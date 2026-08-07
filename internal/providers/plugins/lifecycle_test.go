package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// hasChild reports whether the plugin currently owns a live child process.
// Declared here rather than on the type because nothing outside these tests
// needs to ask.
func (p *ExecPlugin) hasChild() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *ExecPlugin) crashCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.crashes)
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// overlongScript emits a single line far past maxLine before it reads
// anything, which makes bufio.Scanner give up with ErrTooLong.
const overlongScript = `#!/bin/sh
head -c 2000000 /dev/zero | tr '\0' 'x'
echo
while true; do sleep 1; done
`

// orphanScript backgrounds a helper that inherits stdout and then ignores
// every event, so neither the "shutdown" message nor SIGKILL on the direct
// child alone closes the host's read end of the pipe.
const orphanScript = `#!/bin/sh
sleep 60 &
while true; do sleep 1; done
`

// TestExecPluginOverlongLineIsTreatedAsACrash covers the reader giving up on a
// line past maxLine. The scanner error used to be discarded: the loop returned
// while the child was still alive and still writing, so it never exited, never
// counted as a crash, and every later query returned nothing forever.
func TestExecPluginOverlongLineIsTreatedAsACrash(t *testing.T) {
	p := newScriptPlugin(t, "flood", overlongScript, "", Options{
		Timeout:        300 * time.Millisecond,
		RestartBackoff: time.Millisecond,
	})

	got, err := p.Query(context.Background(), "anything")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want no results", got)
	}
	eventually(t, 5*time.Second, "the flooding plugin to be reaped", func() bool {
		return !p.hasChild()
	})
	if p.crashCount() == 0 {
		t.Fatal("a reader failure must be recorded as a crash so backoff and disabling apply")
	}
}

// TestExecPluginShutdownIsTerminal covers a query racing `banshee reload`.
// Host.Load shuts every plugin down and swaps in a fresh set; a plugin that
// could still be restarted afterwards would spawn a child process nothing
// references and nothing ever stops.
func TestExecPluginShutdownIsTerminal(t *testing.T) {
	p := newScriptPlugin(t, "demo", echoScript, "", Options{Timeout: 2 * time.Second})

	if _, err := p.Query(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	p.Shutdown()

	got, err := p.Query(context.Background(), "hello")
	if err != nil || got != nil {
		t.Fatalf("query after Shutdown = (%+v, %v), want (nil, nil)", got, err)
	}
	if p.hasChild() {
		t.Fatal("Shutdown must be terminal: a later query restarted the plugin")
	}
	if err := p.Activate("r1"); err != ErrClosed {
		t.Fatalf("Activate after Shutdown = %v, want ErrClosed", err)
	}
}

// TestExecPluginShutdownIsBounded covers a plugin whose helper keeps the stdout
// pipe open past the direct child's death. Shutdown runs on the GTK main loop
// (Host.Load, from `banshee reload`), so waiting on the reader forever would
// freeze the whole launcher.
func TestExecPluginShutdownIsBounded(t *testing.T) {
	p := newScriptPlugin(t, "orphan", orphanScript, "", Options{Timeout: 100 * time.Millisecond})
	if _, err := p.Query(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { defer close(done); p.Shutdown() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown blocked on a plugin holding its stdout pipe open")
	}
}

// TestExecPluginStartFailureBacksOffAndDisables covers the most likely plugin
// failure of all — a manifest pointing at a binary that is not there. It used
// to bypass the crash bookkeeping entirely, so it was retried (and reported to
// the aggregator) on every single keystroke, forever.
func TestExecPluginStartFailureBacksOffAndDisables(t *testing.T) {
	dir := t.TempDir()
	m := connectors.Manifest{
		V: connectors.ManifestVersion, ID: "ghost", Name: "ghost",
		Type: connectors.TypeExec, Dir: dir,
		Exec: &connectors.ExecSpec{Bin: "./not-a-real-binary"},
	}
	p, err := NewExecPlugin(m, Options{
		Timeout:        50 * time.Millisecond,
		RestartBackoff: time.Millisecond,
		CrashLimit:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Shutdown)

	for i := 0; i < 3; i++ {
		if _, err := p.Query(context.Background(), "anything"); err == nil {
			t.Fatalf("query %d: expected the start failure to surface", i)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !p.Disabled() {
		t.Fatal("a plugin that cannot start must be disabled like any other repeat crasher")
	}
	if got, err := p.Query(context.Background(), "anything"); err != nil || got != nil {
		t.Fatalf("disabled plugin = (%+v, %v), want (nil, nil)", got, err)
	}
}

// TestExecPluginTimeoutIsClamped keeps one plugin from stalling the whole
// launcher: the aggregator joins every provider before it returns.
func TestExecPluginTimeoutIsClamped(t *testing.T) {
	m := connectors.Manifest{
		V: connectors.ManifestVersion, ID: "slow", Name: "slow",
		Type: connectors.TypeExec, Dir: t.TempDir(),
		Exec: &connectors.ExecSpec{Bin: "./plugin.sh", TimeoutMS: 600000},
	}
	p, err := NewExecPlugin(m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Duration(connectors.MaxExecTimeoutMS) * time.Millisecond
	if p.timeout != want {
		t.Fatalf("timeout = %v, want %v", p.timeout, want)
	}
}

// TestHostLoadShutsDownReplacedPlugins is the host-level half of the terminal
// Shutdown: reload must leave no child behind.
func TestHostLoadShutsDownReplacedPlugins(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte(echoScript), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"v":1,"id":"demo","name":"demo","type":"exec","exec":{"bin":"./plugin.sh"}}`
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHost(root, Options{Timeout: 2 * time.Second})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	first := h.ExecPlugins()
	if len(first) != 1 {
		t.Fatalf("loaded %d exec plugins, want 1", len(first))
	}
	if _, err := first[0].Query(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	second := h.ExecPlugins()
	if len(second) != 1 || second[0] == first[0] {
		t.Fatal("Load must build a fresh plugin set")
	}
	if first[0].hasChild() {
		t.Fatal("the replaced plugin's child process outlived the reload")
	}
	if _, err := second[0].Query(context.Background(), "hello"); err != nil {
		t.Fatalf("the reloaded plugin must answer queries: %v", err)
	}
}
