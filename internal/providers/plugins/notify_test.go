package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// notifyScript answers every query with empty results and pushes one notify
// message first; every stdin line is teed to stdin.txt so tests can assert
// the notify-action / notify-closed events land.
const notifyScript = `#!/bin/sh
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$BANSHEE_PLUGIN_DIR/stdin.txt"
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  case "$line" in
    *'"event":"query"'*)
      seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
      printf '{"v":1,"event":"notify","notify":{"id":"n1","summary":"Needs input","body":"b","icon":"icon.png","urgency":"critical","require_input":true,"timeout_ms":0,"actions":[{"key":"default","label":"Focus"}]}}\n'
      printf '{"v":1,"seq":%s,"event":"results","results":[],"done":true}\n' "$seq"
      ;;
  esac
done
`

// badNotifyScript pushes notify messages missing the required fields.
const badNotifyScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  printf '{"v":1,"event":"notify","notify":{"id":"","summary":"no id"}}\n'
  printf '{"v":1,"event":"notify","notify":{"id":"x","summary":""}}\n'
  printf '{"v":1,"event":"notify"}\n'
  printf '{"v":1,"seq":%s,"event":"results","results":[],"done":true}\n' "$seq"
done
`

type sunkNotify struct {
	pluginID string
	n        WireNotify
	respond  func(action string, closed bool, reason int)
}

func TestExecPluginNotifySink(t *testing.T) {
	sunk := make(chan sunkNotify, 4)
	opts := Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, respond func(string, bool, int)) {
			sunk <- sunkNotify{pluginID, n, respond}
		},
	}
	p := newScriptPlugin(t, "cc", notifyScript, "", opts)

	if _, err := p.Query(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	var got sunkNotify
	select {
	case got = <-sunk:
	case <-time.After(2 * time.Second):
		t.Fatal("sink never received the notify message")
	}
	if got.pluginID != "cc" {
		t.Errorf("pluginID = %q, want %q", got.pluginID, "cc")
	}
	n := got.n
	if n.ID != "n1" || n.Summary != "Needs input" || n.Body != "b" {
		t.Errorf("notify = %+v", n)
	}
	if !n.RequireInput || n.Urgency != "critical" {
		t.Errorf("options = require_input:%v urgency:%q", n.RequireInput, n.Urgency)
	}
	if len(n.Actions) != 1 || n.Actions[0].Key != "default" || n.Actions[0].Label != "Focus" {
		t.Errorf("actions = %+v", n.Actions)
	}
	// A relative icon resolves against the plugin dir before the sink sees it.
	if want := filepath.Join(p.m.Dir, "icon.png"); n.Icon != want {
		t.Errorf("icon = %q, want %q", n.Icon, want)
	}

	// respond routes the daemon's signals back onto the plugin's stdin.
	got.respond("default", false, 0)
	got.respond("", true, 2)
	stdinFile := filepath.Join(p.m.Dir, "stdin.txt")
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, _ := os.ReadFile(stdinFile)
		s := string(data)
		if strings.Contains(s, `"event":"notify-action"`) && strings.Contains(s, `"event":"notify-closed"`) {
			if !strings.Contains(s, `"action":"default"`) || !strings.Contains(s, `"reason":2`) {
				t.Fatalf("respond events malformed:\n%s", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("respond events never reached the plugin:\n%s", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecPluginNotifyDropsInvalid(t *testing.T) {
	sunk := make(chan sunkNotify, 4)
	opts := Options{
		Timeout: 2 * time.Second,
		Notify: func(pluginID string, n WireNotify, respond func(string, bool, int)) {
			sunk <- sunkNotify{pluginID, n, respond}
		},
	}
	p := newScriptPlugin(t, "cc", badNotifyScript, "", opts)
	if _, err := p.Query(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sunk:
		t.Fatalf("sink received an invalid notify: %+v", got.n)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestExecPluginNotifyNilSinkSafe(t *testing.T) {
	p := newScriptPlugin(t, "cc", notifyScript, "", Options{Timeout: 2 * time.Second})
	if _, err := p.Query(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	// Nothing to assert beyond "did not panic": the notify line is dropped.
}

// newBackgroundPlugin is newScriptPlugin for a background manifest.
func newBackgroundPlugin(t *testing.T, id, script, prefix string, opts Options) *ExecPlugin {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	m := connectors.Manifest{
		V: connectors.ManifestVersion, ID: id, Name: id, Type: connectors.TypeExec, Dir: dir,
		Exec: &connectors.ExecSpec{Bin: "./plugin.sh", Prefix: prefix, Background: true},
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

// startCountScript records each start then exits, so supervision restarts it
// until the crash limit disables it.
const startCountScript = `#!/bin/sh
echo start >> "$BANSHEE_PLUGIN_DIR/starts.txt"
exit 0
`

func TestExecPluginBackgroundRestartsUntilDisabled(t *testing.T) {
	p := newBackgroundPlugin(t, "bg", startCountScript, "", Options{
		RestartBackoff: 5 * time.Millisecond,
		CrashWindow:    time.Minute,
		CrashLimit:     3,
	})
	p.StartBackground()

	deadline := time.Now().Add(5 * time.Second)
	for !p.Disabled() {
		if time.Now().After(deadline) {
			t.Fatal("plugin never hit the crash limit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(filepath.Join(p.m.Dir, "starts.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "start"); got != 3 {
		t.Errorf("starts = %d, want 3 (one per crash up to the limit)", got)
	}
}

func TestExecPluginStartBackgroundIgnoresNonBackground(t *testing.T) {
	p := newScriptPlugin(t, "fg", startCountScript, "", Options{})
	p.StartBackground()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(p.m.Dir, "starts.txt")); err == nil {
		t.Fatal("non-background plugin was started by StartBackground")
	}
}

func TestHostBackgroundGating(t *testing.T) {
	dir := t.TempDir()
	writeBackgroundPlugin(t, dir, "bg")

	h := NewHost(dir, Options{RestartBackoff: 5 * time.Millisecond})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(dir, "bg", "starts.txt")
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(started); err == nil {
		t.Fatal("Load started a background plugin before StartBackground")
	}

	h.StartBackground()
	waitForFile(t, started)

	// Load after StartBackground re-spawns: the flag is sticky.
	if err := os.Remove(started); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, started)
}

func TestHostProvidersSkipsBackgroundWithoutPrefix(t *testing.T) {
	dir := t.TempDir()
	writeBackgroundPlugin(t, dir, "bg")
	writeManifestPlugin(t, dir, "fg", `{"v":1,"id":"fg","type":"exec","exec":{"bin":"./plugin.sh","prefix":"fg"}}`)
	writeManifestPlugin(t, dir, "bgq", `{"v":1,"id":"bgq","type":"exec","exec":{"bin":"./plugin.sh","prefix":"bgq","background":true}}`)

	h := NewHost(dir, Options{})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, p := range h.Providers() {
		names[p.Name()] = true
	}
	if names["plugin:bg"] {
		t.Error("background plugin without prefix listed as a provider")
	}
	if !names["plugin:fg"] || !names["plugin:bgq"] {
		t.Errorf("providers = %v, want fg and bgq present", names)
	}
}

// --- helpers ----------------------------------------------------------------

func writeBackgroundPlugin(t *testing.T, hostDir, id string) {
	t.Helper()
	writeManifestPlugin(t, hostDir, id,
		`{"v":1,"id":"`+id+`","type":"exec","exec":{"bin":"./plugin.sh","background":true}}`)
}

func writeManifestPlugin(t *testing.T, hostDir, id, manifest string) {
	t.Helper()
	dir := filepath.Join(hostDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte(startCountScript), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
