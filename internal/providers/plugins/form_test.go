package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// formScript answers queries with one form result and records submit events
// (id + values line) to submitted.txt inside the plugin dir.
const formScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"event":"shutdown"'*) exit 0 ;;
    *'"event":"submit"'*)
      printf '%s' "$line" > "$BANSHEE_PLUGIN_DIR/submitted.txt"
      continue ;;
  esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"f1","title":"form demo","form":{"title":"Demo","fields":[{"key":"name","label":"Name","required":true}]}}],"done":true}\n' "$seq"
done
`

func TestWireFormToResult(t *testing.T) {
	m := connectors.Manifest{ID: "wifi", Dir: "/plugins/wifi"}

	t.Run("form converts declaratively and builds a values callback", func(t *testing.T) {
		w := WireResult{ID: "f", Title: "F", Form: &WireForm{
			Title: "Join network",
			Fields: []WireFormField{
				{Key: "ssid", Label: "SSID", Placeholder: "network name", Required: true},
				{Key: "pass", Label: "Password"},
			},
		}}
		res := w.toResult(m)
		if res.Form == nil {
			t.Fatal("Form is nil")
		}
		if res.Form.Title != "Join network" || len(res.Form.Fields) != 2 {
			t.Fatalf("form = %+v", res.Form)
		}
		f0 := res.Form.Fields[0]
		if f0.Key != "ssid" || f0.Label != "SSID" || f0.Placeholder != "network name" || !f0.Required {
			t.Errorf("field 0 = %+v", f0)
		}
		if res.Form.Fields[1].Required {
			t.Error("field 1 should be optional")
		}
		act, err := res.Form.Build(map[string]string{"ssid": "home", "pass": "hunter2"})
		if err != nil {
			t.Fatal(err)
		}
		if act.Kind != providers.ActPluginCallback || act.PluginID != "wifi" || act.ResultID != "f" {
			t.Errorf("action = %+v", act)
		}
		if act.Values["ssid"] != "home" || act.Values["pass"] != "hunter2" {
			t.Errorf("values = %+v", act.Values)
		}
	})

	t.Run("form result ignores a declared action", func(t *testing.T) {
		w := WireResult{ID: "g", Title: "G",
			Action: &WireAction{Kind: KindURL, URL: "https://x"},
			Form:   &WireForm{Title: "T"},
		}
		res := w.toResult(m)
		if res.Form == nil {
			t.Fatal("Form is nil")
		}
		// The launcher never dispatches res.Action while Form is set; the
		// submit path is the form's Build.
		act, err := res.Form.Build(nil)
		if err != nil || act.Kind != providers.ActPluginCallback {
			t.Errorf("Build = (%+v, %v), want plugin callback", act, err)
		}
	})

	t.Run("no form leaves Result.Form nil", func(t *testing.T) {
		if res := (WireResult{ID: "h", Title: "H"}).toResult(m); res.Form != nil {
			t.Errorf("Form = %+v, want nil", res.Form)
		}
	})
}

func TestExecPluginSubmit(t *testing.T) {
	p := newScriptPlugin(t, "demo", formScript, "", Options{Timeout: 2 * time.Second})
	got, err := p.Query(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Form == nil {
		t.Fatalf("results = %+v, want one form result", got)
	}

	if err := p.Submit("f1", map[string]string{"name": "tea"}); err != nil {
		t.Fatal(err)
	}

	stamp := filepath.Join(p.m.Dir, "submitted.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(stamp)
		if err == nil {
			line := string(b)
			if !strings.Contains(line, `"event":"submit"`) ||
				!strings.Contains(line, `"id":"f1"`) ||
				!strings.Contains(line, `"values":{"name":"tea"}`) {
				t.Fatalf("submit event = %s", line)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("plugin never recorded the submit")
}

func TestRegisterCallbackHandlerSubmit(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "demo", `{"v":1,"id":"demo","type":"exec","exec":{"bin":"./plugin.sh"}}`, formScript)
	h := NewHost(root, Options{Timeout: 2 * time.Second})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}

	d := launch.NewDispatcher()
	RegisterCallbackHandler(d, h)

	if _, err := h.ExecPlugins()[0].Query(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	err := d.Dispatch(providers.Action{
		Kind: providers.ActPluginCallback, PluginID: "demo", ResultID: "f1",
		Values: map[string]string{"name": "tea"},
	})
	if err != nil {
		t.Fatal(err)
	}

	stamp := filepath.Join(dir, "submitted.txt")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(stamp); err == nil {
			if !strings.Contains(string(b), `"values":{"name":"tea"}`) {
				t.Fatalf("submit event = %s", b)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dispatcher never delivered the submit")
}
