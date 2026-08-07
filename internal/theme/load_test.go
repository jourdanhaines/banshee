package theme

import (
	"strings"
	"testing"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
)

// roundTrip parses the stylesheet with GTK's own CSS engine and serializes it
// back. GtkCssProvider silently drops declarations it does not understand, so
// anything missing from the serialization is a property GTK4 does not
// implement — the exact failure mode a string-matching test cannot see.
//
// GtkCssProvider needs no display, so this runs headless.
func roundTrip(t *testing.T, cfg config.Config) string {
	t.Helper()
	p := gtk.NewCSSProvider()
	p.LoadFromString(Render(cfg))
	return p.String()
}

func TestGeneratedCSSParses(t *testing.T) {
	// Declarations GTK is expected to keep, in its normalized serialization
	// (shorthands are expanded, colors canonicalized).
	tests := []struct {
		name string
		want string
	}{
		{"panel glass at the configured opacity", "background-color: rgba(17,19,27,0.86)"},
		{"panel width", "min-width: 640px"},
		{"panel drop shadow", "box-shadow: 0 14px 40px rgba(0,0,0,0.5)"},
		{"accent border", "border-top-color: rgba(122,162,247,0.35)"},
		{"panel corner radius", "border-top-left-radius: 14px"},
		{"accent caret in the entry", "caret-color: rgb(122,162,247)"},
		{"selection bar width", "border-left-width: 3px"},
		{"selection bar color", "border-left-color: rgb(122,162,247)"},
		{"selection tint", "background-color: rgba(122,162,247,0.16)"},
		{"dim subtitles", "color: rgba(223,228,238,0.42)"},
		{"badge accent", "color: rgb(122,162,247)"},
		{"icon size", "-gtk-icon-size: 24px"},
		{"row transition", "transition-duration: 90ms"},
		{"undershoot suppressed", "background-image: none"},
	}

	css := roundTrip(t, config.Default())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(css, tt.want) {
				t.Errorf("GTK dropped or never understood %q\n---\n%s", tt.want, css)
			}
		})
	}
}

func TestGeneratedCSSKeepsEverySelector(t *testing.T) {
	selectors := []string{
		"window#banshee-window",
		"#banshee-window .panel",
		"#banshee-window entry.query",
		"#banshee-window entry.query > text > placeholder",
		"#banshee-window entry.query > text > selection",
		"#banshee-window scrolledwindow.results-scroll",
		"#banshee-window list.results",
		"#banshee-window list.results > row",
		"#banshee-window list.results > row:hover",
		"#banshee-window list.results > row:selected",
		"#banshee-window .result-icon",
		"#banshee-window .result-title",
		"#banshee-window .result-subtitle",
		"#banshee-window .result-badge",
		"#banshee-window .empty",
	}

	css := roundTrip(t, config.Default())
	for _, s := range selectors {
		if !strings.Contains(css, s+" {") {
			t.Errorf("selector %q did not survive parsing", s)
		}
	}
}

func TestGeneratedCSSParsesForEveryConfig(t *testing.T) {
	// A stylesheet that parses for the defaults but not for a custom accent
	// would be a template bug; check the interesting configs too.
	cases := map[string]config.Config{
		"zero value":       {},
		"custom accent":    {Accent: "#a78bfa", WindowOpacity: 0.5, LauncherWidth: 900},
		"short hex accent": {Accent: "#f0a", WindowOpacity: 1, LauncherWidth: 320},
		"invalid accent":   {Accent: "chartreuse"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if css := roundTrip(t, cfg); !strings.Contains(css, "#banshee-window .panel {") {
				t.Errorf("stylesheet failed to parse:\n%s", css)
			}
		})
	}
}
