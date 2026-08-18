package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

