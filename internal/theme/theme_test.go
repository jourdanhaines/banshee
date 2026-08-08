package theme

import (
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/config"
)

func TestParamsFor(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want Params
	}{
		{
			name: "defaults",
			cfg:  config.Default(),
			want: Params{Accent: "#7aa2f7", AccentRGB: "122, 162, 247", PanelRGB: "17, 19, 27", Opacity: "0.92", Width: 640},
		},
		{
			name: "zero value config falls back to defaults",
			cfg:  config.Config{},
			want: Params{Accent: "#7aa2f7", AccentRGB: "122, 162, 247", PanelRGB: "17, 19, 27", Opacity: "0.92", Width: 640},
		},
		{
			name: "custom accent, opacity and width",
			cfg:  config.Config{Accent: "#a78bfa", WindowOpacity: 0.5, LauncherWidth: 800},
			want: Params{Accent: "#a78bfa", AccentRGB: "167, 139, 250", PanelRGB: "17, 19, 27", Opacity: "0.5", Width: 800},
		},
		{
			name: "short accent form expands",
			cfg:  config.Config{Accent: "#f00", WindowOpacity: 1, LauncherWidth: 640},
			want: Params{Accent: "#ff0000", AccentRGB: "255, 0, 0", PanelRGB: "17, 19, 27", Opacity: "1", Width: 640},
		},
		{
			name: "invalid accent falls back",
			cfg:  config.Config{Accent: "not-a-color", WindowOpacity: 0.92, LauncherWidth: 640},
			want: Params{Accent: "#7aa2f7", AccentRGB: "122, 162, 247", PanelRGB: "17, 19, 27", Opacity: "0.92", Width: 640},
		},
		{
			name: "out of range opacity and width fall back",
			cfg:  config.Config{Accent: "#7aa2f7", WindowOpacity: 3, LauncherWidth: -10},
			want: Params{Accent: "#7aa2f7", AccentRGB: "122, 162, 247", PanelRGB: "17, 19, 27", Opacity: "0.92", Width: 640},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParamsFor(tt.cfg); got != tt.want {
				t.Errorf("ParamsFor() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRenderSubstitutesParams(t *testing.T) {
	out := Render(config.Config{Accent: "#a78bfa", WindowOpacity: 0.5, LauncherWidth: 800})

	wants := []string{
		"rgba(17, 19, 27, 0.5)",     // panel glass at the configured opacity
		"rgba(167, 139, 250, 0.35)", // accent-derived panel border
		"min-width: 800px",          // configured launcher width
		"color: #a78bfa",            // badge and icons use the accent
		"window#banshee-window",     // window is transparent, panel carries the glass
		"background-color: transparent",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("Render() missing %q\n---\n%s", w, out)
		}
	}
	// The selection bar is gone: selected rows are a background tint only.
	if strings.Contains(out, "border-left") {
		t.Errorf("Render() still emits a border-left selection bar\n---\n%s", out)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	cfg := config.Default()
	if a, b := Render(cfg), Render(cfg); a != b {
		t.Error("Render() is not deterministic for identical configs")
	}
}

func TestRenderNeverEmpty(t *testing.T) {
	// Render swallows template errors by returning ""; guard against that
	// path ever becoming reachable.
	for _, cfg := range []config.Config{{}, config.Default(), {Accent: "#000", WindowOpacity: 1, LauncherWidth: 1}} {
		if Render(cfg) == "" {
			t.Errorf("Render(%+v) returned empty CSS", cfg)
		}
	}
}

func TestRenderBalancedBraces(t *testing.T) {
	out := Render(config.Default())
	if o, c := strings.Count(out, "{"), strings.Count(out, "}"); o != c {
		t.Errorf("unbalanced braces in generated CSS: %d open, %d close", o, c)
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "<no value>") {
		t.Errorf("unexpanded template directives in generated CSS:\n%s", out)
	}
}
