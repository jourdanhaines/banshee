package plugins

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// exampleDir is the in-repo sample plugin shipped as plugins/example.
func exampleDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "..", "plugins", "example"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err != nil {
		t.Skipf("example plugin not present: %v", err)
	}
	return dir
}

// TestExamplePlugin exercises the shipped sample end to end so the docs and
// the host can never drift apart.
func TestExamplePlugin(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(exampleDir(t), filepath.Join(root, "example")); err != nil {
		t.Skipf("cannot stage example plugin: %v", err)
	}

	h := NewHost(root, Options{Timeout: 2 * time.Second})
	t.Cleanup(h.Shutdown)
	if err := h.Load(); err != nil {
		t.Fatal(err)
	}
	provs := h.Providers()
	if len(provs) != 1 {
		t.Fatalf("expected one exec provider, got %+v", provs)
	}

	// The manifest sets prefix "demo": unrelated queries are gated out.
	if got, err := provs[0].Query(context.Background(), "unrelated"); err != nil || got != nil {
		t.Fatalf("prefix gate = (%+v, %v)", got, err)
	}

	got, err := provs[0].Query(context.Background(), "demo tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d results, want 4: %+v", len(got), got)
	}
	want := []struct {
		id   string
		kind string
	}{
		{"plugin:example:hello", providers.ActPluginCallback},
		{"plugin:example:docs", providers.ActURL},
		{"plugin:example:notify", providers.ActExecDetach},
		{"plugin:example:greet", providers.ActPluginCallback},
	}
	for i, w := range want {
		if got[i].ID != w.id || got[i].Action.Kind != w.kind {
			t.Errorf("result %d = (%q, %q), want (%q, %q)", i, got[i].ID, got[i].Action.Kind, w.id, w.kind)
		}
	}
	form := got[3].Form
	if form == nil {
		t.Fatal("greet result carries no form")
	}
	assertExampleFields(t, form.Fields)
	act, err := form.Build(map[string]string{"name": "tea"})
	if err != nil {
		t.Fatal(err)
	}
	if act.Kind != providers.ActPluginCallback || act.PluginID != "example" ||
		act.ResultID != "greet" || act.Values["name"] != "tea" {
		t.Errorf("form action = %+v, want a values-carrying callback", act)
	}
	if got[0].Subtitle != "you typed: tea" {
		t.Errorf("subtitle = %q, want the stripped query", got[0].Subtitle)
	}
	if got[0].Accent != "#7aa2f7" {
		t.Errorf("accent = %q, want the manifest accent", got[0].Accent)
	}
	if got[0].Icon.ThemeName != "utilities-terminal-symbolic" {
		t.Errorf("icon = %+v, want the manifest icon", got[0].Icon)
	}
	if got[1].Icon.ThemeName != "text-x-generic-symbolic" {
		t.Errorf("per-result icon = %+v", got[1].Icon)
	}
}

// assertExampleFields checks the greet form demos every field kind the wire
// carries: entry, dropdown, show_when-gated slider and checkbox.
func assertExampleFields(t *testing.T, fields []providers.FormField) {
	t.Helper()
	tests := []struct {
		name  string
		check func(f providers.FormField) bool
	}{
		{"name", func(f providers.FormField) bool {
			return f.Required && len(f.Options) == 0 && f.Slider == nil && !f.Checkbox
		}},
		{"style", func(f providers.FormField) bool {
			return len(f.Options) == 2 && f.Options[0] == "Plain" && f.Options[1] == "Shout"
		}},
		{"repeat", func(f providers.FormField) bool {
			return f.Slider != nil &&
				*f.Slider == providers.SliderSpec{Min: 1, Max: 5, Step: 1, Default: 1} &&
				reflect.DeepEqual(f.ShowWhen, []providers.Condition{{Key: "style", Values: []string{"Shout"}}})
		}},
		{"notify", func(f providers.FormField) bool {
			return f.Checkbox && f.Checked && f.ShowWhen == nil
		}},
	}
	if len(fields) != len(tests) {
		t.Fatalf("got %d fields, want %d: %+v", len(fields), len(tests), fields)
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if fields[i].Key != tt.name || !tt.check(fields[i]) {
				t.Errorf("field %d = %+v", i, fields[i])
			}
		})
	}
}
