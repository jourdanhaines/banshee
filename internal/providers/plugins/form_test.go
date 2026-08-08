package plugins

import (
	"context"
	"encoding/json"
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
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"f1","title":"form demo","form":{"title":"Demo","fields":[{"key":"name","label":"Name","required":true},{"key":"size","label":"Size","options":["small","large"]}]}}],"done":true}\n' "$seq"
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
				{Key: "band", Label: "Band", Options: []string{"2.4 GHz", "5 GHz"}},
			},
		}}
		res := w.toResult(m)
		if res.Form == nil {
			t.Fatal("Form is nil")
		}
		if res.Form.Title != "Join network" || len(res.Form.Fields) != 3 {
			t.Fatalf("form = %+v", res.Form)
		}
		f0 := res.Form.Fields[0]
		if f0.Key != "ssid" || f0.Label != "SSID" || f0.Placeholder != "network name" || !f0.Required {
			t.Errorf("field 0 = %+v", f0)
		}
		if res.Form.Fields[1].Required {
			t.Error("field 1 should be optional")
		}
		if f2 := res.Form.Fields[2]; len(f2.Options) != 2 || f2.Options[1] != "5 GHz" {
			t.Errorf("field 2 = %+v", f2)
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

// TestWireFormFieldSecret pins the "secret" flag's trip across the wire: a
// plugin that sets it gets a masked field, and one that predates the flag
// keeps the unmasked default (symmetric degradation).
func TestWireFormFieldSecret(t *testing.T) {
	m := connectors.Manifest{ID: "vault", Dir: "/plugins/vault"}

	tests := []struct {
		name string
		json string
		want []bool // Secret per field, in order
	}{
		{
			name: `"secret": true survives the conversion`,
			json: `{"id":"r","title":"R","form":{"title":"Unlock","fields":[{"key":"pass","label":"Password","secret":true}]}}`,
			want: []bool{true},
		},
		{
			name: "absent secret stays false",
			json: `{"id":"r","title":"R","form":{"title":"Unlock","fields":[{"key":"user","label":"User"}]}}`,
			want: []bool{false},
		},
		{
			name: `explicit "secret": false stays false`,
			json: `{"id":"r","title":"R","form":{"title":"Unlock","fields":[{"key":"user","label":"User","secret":false}]}}`,
			want: []bool{false},
		},
		{
			name: "mixed fields keep their own flag",
			json: `{"id":"r","title":"R","form":{"title":"Login","fields":[{"key":"user","label":"User"},{"key":"pass","label":"Password","secret":true},{"key":"note","label":"Note"}]}}`,
			want: []bool{false, true, false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w WireResult
			if err := json.Unmarshal([]byte(tt.json), &w); err != nil {
				t.Fatal(err)
			}
			res := w.toResult(m)
			if res.Form == nil {
				t.Fatal("Form is nil")
			}
			if len(res.Form.Fields) != len(tt.want) {
				t.Fatalf("got %d fields, want %d", len(res.Form.Fields), len(tt.want))
			}
			for i, want := range tt.want {
				if got := res.Form.Fields[i].Secret; got != want {
					t.Errorf("field %d (%s) Secret = %v, want %v",
						i, res.Form.Fields[i].Key, got, want)
				}
			}
		})
	}
}

// TestWireFormFieldOptions pins the "options" list's trip across the wire: a
// plugin that sets it gets a dropdown over exactly those choices in that
// order, and one that omits it (or sends an empty list) keeps the free-text
// entry. The contract keys off the option count, so absent and empty are the
// same answer — both leave nothing to choose from.
func TestWireFormFieldOptions(t *testing.T) {
	m := connectors.Manifest{ID: "vault", Dir: "/plugins/vault"}

	tests := []struct {
		name string
		json string
		want [][]string // Options per field, in order
	}{
		{
			name: `"options" survives the conversion in order`,
			json: `{"id":"r","title":"R","form":{"title":"Store","fields":[{"key":"backend","label":"Storage","options":["keyring","plaintext","nimbus"]}]}}`,
			want: [][]string{{"keyring", "plaintext", "nimbus"}},
		},
		{
			name: "absent options stays empty",
			json: `{"id":"r","title":"R","form":{"title":"Store","fields":[{"key":"name","label":"Name"}]}}`,
			want: [][]string{{}},
		},
		{
			name: `explicit "options": [] stays empty`,
			json: `{"id":"r","title":"R","form":{"title":"Store","fields":[{"key":"name","label":"Name","options":[]}]}}`,
			want: [][]string{{}},
		},
		{
			name: "mixed fields keep their own list",
			json: `{"id":"r","title":"R","form":{"title":"Store","fields":[{"key":"name","label":"Name"},{"key":"backend","label":"Storage","options":["keyring","nimbus"]},{"key":"pass","label":"Password","secret":true}]}}`,
			want: [][]string{{}, {"keyring", "nimbus"}, {}},
		},
		{
			name: "a single option is still a dropdown",
			json: `{"id":"r","title":"R","form":{"title":"Store","fields":[{"key":"backend","label":"Storage","options":["keyring"]}]}}`,
			want: [][]string{{"keyring"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w WireResult
			if err := json.Unmarshal([]byte(tt.json), &w); err != nil {
				t.Fatal(err)
			}
			res := w.toResult(m)
			if res.Form == nil {
				t.Fatal("Form is nil")
			}
			if len(res.Form.Fields) != len(tt.want) {
				t.Fatalf("got %d fields, want %d", len(res.Form.Fields), len(tt.want))
			}
			for i, want := range tt.want {
				got := res.Form.Fields[i].Options
				if len(got) != len(want) {
					t.Errorf("field %d (%s) Options = %q, want %q",
						i, res.Form.Fields[i].Key, got, want)
					continue
				}
				for j := range want {
					if got[j] != want[j] {
						t.Errorf("field %d (%s) Options[%d] = %q, want %q",
							i, res.Form.Fields[i].Key, j, got[j], want[j])
					}
				}
			}
		})
	}
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
	// The dropdown field proves options survive the real stdout → decode path,
	// not just an in-process struct literal.
	if fields := got[0].Form.Fields; len(fields) != 2 ||
		len(fields[1].Options) != 2 || fields[1].Options[0] != "small" {
		t.Fatalf("form fields = %+v", got[0].Form.Fields)
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
