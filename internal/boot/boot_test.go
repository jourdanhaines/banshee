package boot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/providers"
)

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
		want := []string{"lastaction", "sessions", "connectors", "repos", "calc", "apps", "procs", "plugins"}
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
