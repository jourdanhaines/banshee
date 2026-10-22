package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
				{Key: "width", Label: "Width",
					Slider:   &WireSlider{Min: 20, Max: 80, Step: 20, Default: 40},
					ShowWhen: []WireCondition{{Key: "band", Values: []string{"5 GHz"}}}},
			},
			SubmitLabel: "Join",
		}}
		res := w.toResult(m)
		if res.Form == nil {
			t.Fatal("Form is nil")
		}
		if res.Form.Title != "Join network" || len(res.Form.Fields) != 4 || res.Form.SubmitLabel != "Join" {
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
		f3 := res.Form.Fields[3]
		if f3.Slider == nil || *f3.Slider != (providers.SliderSpec{Min: 20, Max: 80, Step: 20, Default: 40}) {
			t.Errorf("field 3 Slider = %+v", f3.Slider)
		}
		if len(f3.ShowWhen) != 1 || f3.ShowWhen[0].Key != "band" || !slices.Equal(f3.ShowWhen[0].Values, []string{"5 GHz"}) {
			t.Errorf("field 3 ShowWhen = %+v", f3.ShowWhen)
		}
		// Build forwards whatever it is handed: show_when filtering is the
		// launcher's job, so a hidden field's value passes through untouched.
		act, err := res.Form.Build(map[string]string{"ssid": "home", "pass": "hunter2", "band": "2.4 GHz", "width": "40"})
		if err != nil {
			t.Fatal(err)
		}
		if act.Kind != providers.ActPluginCallback || act.PluginID != "wifi" || act.ResultID != "f" {
			t.Errorf("action = %+v", act)
		}
		if act.Values["ssid"] != "home" || act.Values["pass"] != "hunter2" || act.Values["width"] != "40" {
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
		if res.Form.SubmitLabel != "" {
			t.Errorf("SubmitLabel = %q, want empty when omitted", res.Form.SubmitLabel)
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

// decodeField unmarshals a one-field result and returns the converted field.
func decodeField(t *testing.T, field string) providers.FormField {
	t.Helper()
	var w WireResult
	js := `{"id":"r","title":"R","form":{"title":"T","fields":[` + field + `]}}`
	if err := json.Unmarshal([]byte(js), &w); err != nil {
		t.Fatal(err)
	}
	res := w.toResult(connectors.Manifest{ID: "gen", Dir: "/plugins/gen"})
	if res.Form == nil || len(res.Form.Fields) != 1 {
		t.Fatalf("form = %+v", res.Form)
	}
	return res.Form.Fields[0]
}

// TestWireFormFieldSlider pins the "slider" spec's trip across the wire.
func TestWireFormFieldSlider(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  *providers.SliderSpec
	}{
		{
			name:  "full spec round-trips",
			field: `{"key":"len","label":"Length","slider":{"min":8,"max":128,"step":2,"default":24}}`,
			want:  &providers.SliderSpec{Min: 8, Max: 128, Step: 2, Default: 24},
		},
		{
			name:  "absent slider stays nil",
			field: `{"key":"len","label":"Length"}`,
		},
		{
			name:  "null slider stays nil",
			field: `{"key":"len","label":"Length","slider":null}`,
		},
		{
			name:  "unknown inner key is ignored",
			field: `{"key":"len","label":"Length","slider":{"min":1,"max":5,"log":true}}`,
			want:  &providers.SliderSpec{Min: 1, Max: 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeField(t, tt.field).Slider
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("Slider = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWireFormFieldCheckbox pins the "checkbox" and "checked" flags.
func TestWireFormFieldCheckbox(t *testing.T) {
	tests := []struct {
		name         string
		field        string
		wantCheckbox bool
		wantChecked  bool
	}{
		{"checkbox and checked", `{"key":"sym","label":"Symbols","checkbox":true,"checked":true}`, true, true},
		{"checkbox only", `{"key":"sym","label":"Symbols","checkbox":true}`, true, false},
		{"absent", `{"key":"sym","label":"Symbols"}`, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := decodeField(t, tt.field)
			if f.Checkbox != tt.wantCheckbox || f.Checked != tt.wantChecked {
				t.Errorf("Checkbox, Checked = %v, %v; want %v, %v",
					f.Checkbox, f.Checked, tt.wantCheckbox, tt.wantChecked)
			}
		})
	}
}

// TestWireFormFieldShowWhen pins the "show_when" condition list's trip.
func TestWireFormFieldShowWhen(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  []providers.Condition
	}{
		{
			name:  "conditions decode in order",
			field: `{"key":"x","label":"X","show_when":[{"key":"gen","values":["Key","JWT secret"]},{"key":"sym","values":["true"]}]}`,
			want: []providers.Condition{
				{Key: "gen", Values: []string{"Key", "JWT secret"}},
				{Key: "sym", Values: []string{"true"}},
			},
		},
		{
			name:  "absent stays nil",
			field: `{"key":"bits","label":"Bits"}`,
		},
		{
			name:  "empty list stays nil",
			field: `{"key":"bits","label":"Bits","show_when":[]}`,
		},
		{
			name:  "unknown inner key is ignored",
			field: `{"key":"bits","label":"Bits","show_when":[{"key":"gen","values":["Key"],"negate":true}]}`,
			want:  []providers.Condition{{Key: "gen", Values: []string{"Key"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeField(t, tt.field).ShowWhen
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ShowWhen = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// showWhenObjectScript answers with a result whose show_when is an object
// rather than a list, followed by a well-formed message for the same seq.
const showWhenObjectScript = `#!/bin/sh
while IFS= read -r line; do
  case "$line" in *'"event":"shutdown"'*) exit 0 ;; esac
  seq=$(printf '%s' "$line" | sed -n 's/.*"seq":\([0-9]*\).*/\1/p')
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"bad","title":"bad","form":{"title":"T","fields":[{"key":"a","show_when":{"key":"g","values":["x"]}}]}}]}\n' "$seq"
  printf '{"v":1,"seq":%s,"event":"results","results":[{"id":"good","title":"good"}],"done":true}\n' "$seq"
done
`

// TestShowWhenObjectDropsWholeMessage pins how the host treats the object
// shape of show_when: it is a JSON type error, so the entire results message
// carrying it is skipped as malformed while later messages still land.
func TestShowWhenObjectDropsWholeMessage(t *testing.T) {
	p := newScriptPlugin(t, "sw", showWhenObjectScript, "", Options{Timeout: 2 * time.Second})
	got, err := p.Query(context.Background(), "q")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "good" {
		t.Fatalf("results = %+v, want only the well-formed message's result", got)
	}
}

// TestWireFormFieldLegacy pins forward and backward compatibility of the new
// field keys: a legacy field decodes with every new field zero, and a field
// that sets none of them marshals without their keys.
func TestWireFormFieldLegacy(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"legacy field decodes with new fields zero", func(t *testing.T) {
			f := decodeField(t, `{"key":"name","label":"Name","placeholder":"x","required":true,"secret":false}`)
			if f.Slider != nil || f.Checkbox || f.Checked || len(f.ShowWhen) != 0 {
				t.Errorf("field = %+v, want new fields zero", f)
			}
		}},
		{"zero new fields are omitted on marshal", func(t *testing.T) {
			b, err := json.Marshal(WireFormField{Key: "k"})
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"slider", "checkbox", "checked", "show_when"} {
				if strings.Contains(string(b), `"`+key+`"`) {
					t.Errorf("marshal = %s, carries %q", b, key)
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
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
