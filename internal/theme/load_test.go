package theme

import (
	"sort"
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
		{"panel glass at the configured opacity", "background-color: rgba(17,19,27,0.92)"},
		{"panel width", "min-width: 640px"},
		{"panel drop shadow", "box-shadow: 0 14px 40px rgba(0,0,0,0.5)"},
		{"accent border", "border-top-color: rgba(122,162,247,0.35)"},
		{"panel corner radius", "border-top-left-radius: 14px"},
		{"accent caret in the entry", "caret-color: rgb(122,162,247)"},
		{"selection tint", "background-color: rgba(122,162,247,0.16)"},
		{"accent-tinted icons", "color: rgb(122,162,247)"},
		{"dim subtitles", "color: rgba(223,228,238,0.42)"},
		{"badge accent", "color: rgb(122,162,247)"},
		{"icon size", "-gtk-icon-size: 24px"},
		{"row transition", "transition-duration: 90ms"},
		{"undershoot suppressed", "background-image: none"},
		{"drain bar thickness", "min-height: 3px"},
		{"per-row drain bar thickness", "min-height: 2px"},
		{"per-row drain bar accent", "background-color: rgba(122,162,247,0.75)"},
		{"slider trough thickness", "min-height: 6px"},
		{"slider handle overhangs the trough", "margin-top: -5px"},
		{"slider fill accent", "background-color: rgba(122,162,247,0.6)"},
		{"slider focus ring", "box-shadow: 0 0 0 3px rgba(122,162,247,0.3)"},
		{"checked checkbox accent", "background-color: rgba(122,162,247,0.85)"},
		{"dropdown list stays transparent", "background-color: rgba(0,0,0,0)"},
		{"dropdown selected hover tint", "background-color: rgba(122,162,247,0.22)"},
		{"generated secret is monospace", `font-family: "Adwaita Mono", "JetBrainsMono Nerd Font", "monospace"`},
		{"generated secret is bold", "font-weight: 700"},
		{"generated buttons are square", "min-width: 28px"},
		{"picker cells are large glyphs", "font-size: 28px"},
		{"picker variant popover radius", "border-top-left-radius: 12px"},
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
		"#banshee-window entry.form-field",
		"#banshee-window entry.error.form-field",
		"#banshee-window .form-view",
		"#banshee-window .form-title",
		"#banshee-window .form-label",
		"#banshee-window .form-hint",
		"#banshee-window dropdown.form-field",
		"#banshee-window dropdown.form-field > button",
		"#banshee-window dropdown.form-field > button:hover",
		"#banshee-window dropdown.form-field > popover > contents",
		"#banshee-window dropdown.form-field > popover listview > row",
		"#banshee-window dropdown.form-field > popover listview > row:selected",
		"#banshee-window dropdown.form-field > popover listview",
		"#banshee-window dropdown.form-field > popover scrolledwindow",
		"#banshee-window dropdown.form-field > popover scrolledwindow > viewport",
		"#banshee-window dropdown.form-field > popover listview > row:hover",
		"#banshee-window dropdown.form-field > popover listview > row:selected:hover",
		"#banshee-window dropdown.form-field > popover > arrow",
		"#banshee-window button.form-submit",
		"#banshee-window button.form-back",
		// GTK serializes pseudo-classes ahead of classes, so
		"#banshee-window button:focus.form-submit",
		"#banshee-window button:hover.form-submit",
		"#banshee-window button:focus.form-back",
		"#banshee-window button:hover.form-back",
		"#banshee-window scale.form-field",
		"#banshee-window scale.form-field > trough",
		"#banshee-window scale.form-field > trough > highlight",
		"#banshee-window scale.form-field > trough > slider",
		"#banshee-window scale.form-field > trough > slider:hover",
		"#banshee-window scale:focus-within.form-field > trough > slider",
		"#banshee-window scale.form-field > value",
		"#banshee-window checkbutton.form-field",
		"#banshee-window checkbutton.form-field > check",
		"#banshee-window checkbutton:hover.form-field > check",
		"#banshee-window checkbutton:focus-within.form-field > check",
		"#banshee-window checkbutton.form-field > check:checked",
		"#banshee-window checkbutton.form-field > label",
		"#banshee-window box.form-generated-row",
		"#banshee-window label.form-generated",
		"#banshee-window button.form-generated-toggle",
		"#banshee-window button.form-generated-reroll",
		"#banshee-window button:hover.form-generated-toggle",
		"#banshee-window button:hover.form-generated-reroll",
		"#banshee-window button:focus.form-generated-toggle",
		"#banshee-window button:focus.form-generated-reroll",
		"#banshee-window button:checked.form-generated-toggle",
		"#banshee-window progressbar.code-timer",
		"#banshee-window progressbar.code-timer > trough",
		"#banshee-window progressbar.code-timer > trough > progress",
		"#banshee-window progressbar.row-timer",
		"#banshee-window progressbar.row-timer > trough",
		"#banshee-window progressbar.row-timer > trough > progress",
		"#banshee-window scrolledwindow.results-scroll",
		"#banshee-window list.results",
		"#banshee-window list.results > row",
		"#banshee-window list.results > row:hover",
		"#banshee-window list.results > row:selected",
		"#banshee-window .result-icon",
		"#banshee-window .result-title",
		"#banshee-window .result-subtitle",
		"#banshee-window .result-badge",
		"#banshee-window .result-preview",
		"#banshee-window label.empty",
		"#banshee-window entry.picker-query",
		"#banshee-window entry:focus.picker-query",
		"#banshee-window entry:focus-within.picker-query",
		"#banshee-window entry.picker-query > text > placeholder",
		"#banshee-window entry.picker-query > text > selection",
		"#banshee-window button.picker-back",
		"#banshee-window button:focus.picker-back",
		"#banshee-window button:hover.picker-back",
		"#banshee-window .picker-view",
		"#banshee-window scrolledwindow.picker-scroll",
		"#banshee-window scrolledwindow.picker-scroll > viewport",
		"#banshee-window flowbox.picker-grid",
		"#banshee-window scrolledwindow.picker-scroll undershoot.top",
		"#banshee-window scrolledwindow.picker-scroll undershoot.bottom",
		"#banshee-window flowbox.picker-grid > flowboxchild",
		"#banshee-window flowbox.picker-grid > flowboxchild:hover",
		"#banshee-window flowbox.picker-grid > flowboxchild:selected",
		"#banshee-window flowbox.picker-grid > flowboxchild:selected:hover",
		"#banshee-window label.picker-cell",
		"#banshee-window popover.picker-variants > contents",
		"#banshee-window popover.picker-variants > arrow",
	}

	parsed := map[string]bool{}
	for _, line := range strings.Split(roundTrip(t, config.Default()), "\n") {
		if sel, ok := strings.CutSuffix(strings.TrimSpace(line), " {"); ok {
			parsed[canonicalSelector(sel)] = true
		}
	}
	for _, s := range selectors {
		if !parsed[canonicalSelector(s)] {
			t.Errorf("selector %q did not survive parsing", s)
		}
	}
}

// canonicalSelector sorts the classes and pseudo-classes inside each compound
// selector. GTK re-serializes them in interned-quark order, which shifts as
// stylesheets add classes, so only the set within a compound is stable.
func canonicalSelector(sel string) string {
	parts := strings.Fields(sel)
	for i, part := range parts {
		start := strings.IndexAny(part, ".:")
		if start < 0 {
			continue
		}
		var tokens []string
		rest := part[start:]
		for rest != "" {
			end := strings.IndexAny(rest[1:], ".:") + 1
			if end == 0 {
				end = len(rest)
			}
			tokens = append(tokens, rest[:end])
			rest = rest[end:]
		}
		sort.Strings(tokens)
		parts[i] = part[:start] + strings.Join(tokens, "")
	}
	return strings.Join(parts, " ")
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
