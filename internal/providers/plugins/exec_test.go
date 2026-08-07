package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// echoScript answers every query with a single result echoing the query, and
// records activations to activated.txt inside the plugin dir.
const echoScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"event":"shutdown"'*) exit 0 ;;
    *'"event":"activate"'*)
      printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' > "$BANSHEE_PLUGIN_DIR/activated.txt"
      continue ;;
  esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  q=$(printf '%s' "$line" | sed -n 's/.*"query":"\([^"]*\)".*/\1/p')
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"r1","title":"echo %s","subtitle":"sub","score":77,"action":{"kind":"url","url":"https://example.com/%s"}}],"done":true}\n' "$seq" "$q" "$q"
done
`

// staleSeqScript always answers with a sequence number the host never asked
// for, so every message must be discarded.
const staleSeqScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  printf '{"v":1,"seq":9999,"event":"results","results":[{"id":"ghost","title":"ghost"}],"done":true}\n'
done
`

// partialScript sends one result but never sets done, so the host must return
// what it has when the soft timeout elapses.
const partialScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"p1","title":"partial"}],"done":false}\n' "$seq"
done
`

// crashScript exits non-zero after reading one event.
const crashScript = `#!/bin/sh
IFS= read -r line
exit 1
`

// noisyScript emits garbage and unknown events alongside a valid answer.
const noisyScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  printf 'not json at all\n'
  printf '{"v":1,"seq":%s,"event":"activated"}\n' "$seq"
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"n1","title":"noisy"},{"id":"n2","title":""}],"done":true}\n' "$seq"
done
`

// newScriptPlugin writes a manifest + script into a fresh plugin dir and
// returns a started-on-demand ExecPlugin.
func newScriptPlugin(t *testing.T, id, script, prefix string, opts Options) *ExecPlugin {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := connectors.Manifest{
		V: connectors.ManifestVersion, ID: id, Name: id, Type: connectors.TypeExec, Dir: dir,
		Exec: &connectors.ExecSpec{Bin: "./plugin.sh", Prefix: prefix},
	}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := NewExecPlugin(m, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Shutdown)
	return p
}

func TestExecPluginQuery(t *testing.T) {
	p := newScriptPlugin(t, "demo", echoScript, "demo", Options{Timeout: 2 * time.Second})

	got, err := p.Query(context.Background(), "demo hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.ID != "plugin:demo:r1" || r.Title != "echo hello" || r.Subtitle != "sub" || r.Score != 77 {
		t.Fatalf("unexpected result: %+v", r)
	}
	if r.Category != providers.CatPlugin {
		t.Fatalf("category = %d, want CatPlugin", r.Category)
	}
	if r.Action.Kind != providers.ActURL || r.Action.URL != "https://example.com/hello" {
		t.Fatalf("action = %+v", r.Action)
	}

	// Prefix gate: unrelated queries never reach the plugin.
	if got, err := p.Query(context.Background(), "unrelated"); err != nil || got != nil {
		t.Fatalf("prefix gate = (%+v, %v), want (nil, nil)", got, err)
	}

	// A second query on the live process still works (seq advances).
	got, err = p.Query(context.Background(), "demo again")
	if err != nil || len(got) != 1 || got[0].Title != "echo again" {
		t.Fatalf("second query = (%+v, %v)", got, err)
	}
}

func TestExecPluginActivate(t *testing.T) {
	p := newScriptPlugin(t, "demo", echoScript, "", Options{Timeout: 2 * time.Second})
	if _, err := p.Query(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if err := p.Activate("r1"); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(p.m.Dir, "activated.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(stamp); err == nil && strings.TrimSpace(string(b)) == "r1" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("plugin never recorded the activation")
}

func TestExecPluginStaleSeqDiscarded(t *testing.T) {
	p := newScriptPlugin(t, "stale", staleSeqScript, "", Options{Timeout: 200 * time.Millisecond})
	got, err := p.Query(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("results for a stale seq must be discarded, got %+v", got)
	}
}

func TestExecPluginSoftTimeoutMergesPartials(t *testing.T) {
	p := newScriptPlugin(t, "partial", partialScript, "", Options{Timeout: 300 * time.Millisecond})
	start := time.Now()
	got, err := p.Query(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "partial" {
		t.Fatalf("got %+v, want the partial result", got)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("returned after %v, want at least the soft timeout", elapsed)
	}
}

func TestExecPluginIgnoresGarbage(t *testing.T) {
	p := newScriptPlugin(t, "noisy", noisyScript, "", Options{Timeout: 2 * time.Second})
	got, err := p.Query(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "noisy" {
		t.Fatalf("got %+v, want only the titled result", got)
	}
}

func TestExecPluginCrashDisables(t *testing.T) {
	p := newScriptPlugin(t, "crashy", crashScript, "", Options{
		Timeout:        500 * time.Millisecond,
		RestartBackoff: time.Millisecond,
		CrashWindow:    30 * time.Second,
		CrashLimit:     3,
	})
	for i := 0; i < 3; i++ {
		if _, err := p.Query(context.Background(), "boom"); err != nil {
			t.Fatalf("query %d: unexpected error %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !p.Disabled() {
		t.Fatal("plugin should be disabled after 3 crashes in the window")
	}
	got, err := p.Query(context.Background(), "boom")
	if err != nil || got != nil {
		t.Fatalf("disabled plugin = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestExecPluginContextCancel(t *testing.T) {
	p := newScriptPlugin(t, "partial", partialScript, "", Options{Timeout: 5 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := p.Query(ctx, "anything"); err == nil {
		t.Fatal("expected context error")
	}
}

func TestNewExecPluginRejectsURLManifest(t *testing.T) {
	m := connectors.Manifest{V: 1, ID: "x", Type: connectors.TypeURL, URL: &connectors.URLSpec{Template: "https://x"}}
	if _, err := NewExecPlugin(m, Options{}); err == nil {
		t.Fatal("expected error for a url manifest")
	}
}
