package boot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// keysOf lists the result IDs a query produced, sorted, so a failure message
// says what the aggregator actually returned instead of only what was missing.
func keysOf(m map[string]providers.Result) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// isolate points every XDG path at a scratch directory so the assembled stack
// never reads or writes the developer's real configuration.
func isolate(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return root
}

// TestNewWiring exercises the real object graph the daemon runs on — every
// provider, the aggregator and the dispatcher — without constructing the GTK
// window. It is the integration counterpart to each package's unit tests: it
// catches a provider that was never registered, an action kind with no
// handler, and a boot-time panic.
func TestNewWiring(t *testing.T) {
	root := isolate(t)

	// A repo for the repo-derived providers to match on.
	repo := filepath.Join(root, "dev", "blacksheep")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.SearchPaths = []string{filepath.Join(root, "dev")}
	cfg.MaxResults = 30

	b := New(cfg)
	t.Cleanup(b.host.Shutdown)

	t.Run("every provider is registered", func(t *testing.T) {
		want := []string{"lastaction", "sessions", "connectors", "repos", "calc", "totp", "apps", "procs", "plugins"}
		got := map[string]bool{}
		for _, p := range b.reg.Providers() {
			got[p.Name()] = true
		}
		for _, name := range want {
			if !got[name] {
				t.Errorf("provider %q not registered (have %v)", name, got)
			}
		}
	})

	t.Run("every emitted action kind has a handler", func(t *testing.T) {
		// Registering a provider but forgetting its handler is the classic
		// integration bug: it only shows up as a failed activation at runtime.
		kinds := []string{
			providers.ActExecDetach,
			providers.ActTerminal,
			providers.ActURL,
			providers.ActSignal,
			providers.ActPluginCallback,
			providers.ActClipboardCopy,
			"app-launch",
			"kill-procs",
			"totp-copy",
			"totp-add",
			"totp-setup",
			"totp-wizard-reset",
		}
		for _, kind := range kinds {
			// Dispatch with an empty payload: a registered handler rejects it
			// with its own error, an unregistered one reports "no handler".
			err := b.disp.Dispatch(providers.Action{Kind: kind})
			if err != nil && strings.Contains(err.Error(), "no handler for action kind") {
				t.Errorf("no handler registered for action kind %q", kind)
			}
		}
	})

	t.Run("repo query yields the repo block", func(t *testing.T) {
		res := b.Aggregator().Query(context.Background(), "blacksh")
		// sessions + repos always fire; connectors needs a binding, which this
		// bare repo has none of.
		wantCats := map[providers.Category]bool{
			providers.CatSession:   false,
			providers.CatDirectory: false,
		}
		for _, r := range res {
			if _, ok := wantCats[r.Category]; ok {
				wantCats[r.Category] = true
			}
		}
		for cat, seen := range wantCats {
			if !seen {
				t.Errorf("no result in category %v for %q (got %d results)", cat, "blacksh", len(res))
			}
		}
		// The block must lead: repo-derived rows share the repo's score, so
		// nothing weaker may sort above them.
		if len(res) > 0 && res[0].Category > providers.CatDirectory {
			t.Errorf("first result is %v %q, want a repo-derived row", res[0].Category, res[0].Title)
		}
	})

	t.Run("empty query is cheap and sorted", func(t *testing.T) {
		res := b.Aggregator().Query(context.Background(), "")
		if len(res) > cfg.MaxResults {
			t.Errorf("got %d results, want <= %d", len(res), cfg.MaxResults)
		}
		for i := 1; i < len(res); i++ {
			if res[i-1].Score < res[i].Score {
				t.Fatalf("results not sorted by descending score at %d: %+v", i, res[i-1:i+1])
			}
		}
	})

	t.Run("a recorded TOTP failure reaches the registered provider", func(t *testing.T) {
		// End-to-end proof that the provider and the action handlers share one
		// SetupState instance: nothing here touches the provider directly, the
		// failure is recorded on the Launcher's field and read back out of the
		// real aggregator. Wiring the two to separate instances compiles fine
		// and would only ever show up as a wizard that never appears.
		if b.totpSetup == nil {
			t.Fatal("New did not create the shared TOTP setup state")
		}
		t.Cleanup(b.totpSetup.Clear)
		b.totpSetup.Fail("keyring", errors.New("collection is locked"))

		got := map[string]providers.Result{}
		for _, r := range b.Aggregator().Query(context.Background(), "totp") {
			got[r.ID] = r
		}

		status, ok := got["totp:wizard:status"]
		if !ok {
			t.Fatalf("no wizard status row for %q (got %v)", "totp", keysOf(got))
		}
		if !strings.Contains(status.Subtitle, "collection is locked") {
			t.Errorf("status subtitle = %q, want the recorded error in it", status.Subtitle)
		}

		retry, ok := got["totp:wizard:retry"]
		if !ok {
			t.Fatalf("no wizard retry row (got %v)", keysOf(got))
		}
		wantAction := providers.Action{Kind: "totp-setup", Argv: []string{"keyring"}}
		if !reflect.DeepEqual(retry.Action, wantAction) {
			t.Errorf("retry action = %+v, want %+v", retry.Action, wantAction)
		}

		// Isolated XDG paths mean there is no totp index on disk, so this is
		// first-time setup and the escape row back to the chooser must be there.
		if _, ok := got["totp:wizard:back"]; !ok {
			t.Errorf("no wizard back row during first-time setup (got %v)", keysOf(got))
		}
	})

	t.Run("cancelled query returns promptly", func(t *testing.T) {
		// The aggregator drops cancelled providers' results rather than
		// reporting an error; what matters is that it returns at all instead
		// of blocking on a provider that ignored the cancellation.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			b.Aggregator().Query(ctx, "blacksh")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled query did not return within 5s")
		}
	})
}

func TestReloadIsSafe(t *testing.T) {
	isolate(t)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	b := New(config.Default())
	t.Cleanup(func() {
		b.waitBackground()
		b.host.Shutdown()
	})

	// No banshee.conf on disk: reload must fall back to defaults rather than
	// failing, and must not panic with a nil window.
	if err := b.reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if b.win != nil {
		t.Fatal("reload created a window outside the GTK main loop")
	}
	// The rescan and the plugin restart are deliberately off the main loop, so
	// reload must have returned before they finished — and they must still
	// finish. waitBackground is what the daemon's exit path joins on.
	b.waitBackground()
}

// TestReloadDoesNotBlockOnTheMainLoop pins the split introduced to keep a full
// filesystem walk and a plugin teardown off the GTK main thread: reload itself
// must return promptly, whatever the background half is doing.
func TestReloadDoesNotBlockOnTheMainLoop(t *testing.T) {
	isolate(t)
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	b := New(config.Default())
	t.Cleanup(func() {
		b.waitBackground()
		b.host.Shutdown()
	})

	done := make(chan error, 1)
	go func() { done <- b.reload() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not return within 10s")
	}
}
