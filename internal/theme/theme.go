// Package theme renders banshee's launcher stylesheet and installs it into
// GTK. The look is a dark, blurred-glass panel (the blur itself comes from the
// compositor via the banshee layerrule block) with a single configurable blue
// accent, in the spirit of yazi.
//
// Everything the user can tune — accent, window_opacity, launcher_width —
// flows in from config.Config, so a theme change is a config edit plus a
// daemon reload. CSS generation is a pure function (Render) and is unit
// tested; only Load touches GTK.
package theme

import (
	"strconv"
	"strings"
	"text/template"

	"github.com/jourdanhaines/banshee/internal/config"
)

// Panel background is fixed: the accent tints borders and highlights, but the
// glass itself stays neutral so any accent hue stays readable.
const (
	panelR = 17
	panelG = 19
	panelB = 27
)

// Defaults mirroring config.Default(), applied when a value is missing or
// nonsensical (a zero-value Config must still produce a usable window).
const (
	defaultAccent  = "#7aa2f7"
	defaultOpacity = 0.86
	defaultWidth   = 640
)

// Params is the sanitized, template-ready view of the theme knobs. It is
// exported so tests (and a future `banshee doctor --theme`) can inspect what a
// config actually resolves to.
type Params struct {
	// Accent is the validated accent color as "#rrggbb".
	Accent string
	// AccentRGB is the accent as "r, g, b" for use inside rgba() literals.
	AccentRGB string
	// PanelRGB is the fixed glass color as "r, g, b".
	PanelRGB string
	// Opacity is the panel alpha, already clamped to (0, 1].
	Opacity string
	// Width is the panel's min-width in pixels.
	Width int
}

// ParamsFor derives the template parameters from cfg, substituting defaults
// for an unset or invalid accent, a non-positive or >1 opacity, and a
// non-positive width. It never fails.
func ParamsFor(cfg config.Config) Params {
	accent, ok := ParseHexColor(cfg.Accent)
	if !ok {
		accent, _ = ParseHexColor(defaultAccent)
	}

	opacity := cfg.WindowOpacity
	if opacity <= 0 || opacity > 1 {
		opacity = defaultOpacity
	}

	width := cfg.LauncherWidth
	if width <= 0 {
		width = defaultWidth
	}

	return Params{
		Accent:    accent.Hex(),
		AccentRGB: accent.List(),
		PanelRGB:  RGB{R: panelR, G: panelG, B: panelB}.List(),
		Opacity:   formatAlpha(opacity),
		Width:     width,
	}
}

// Render returns the launcher stylesheet for cfg. It is deterministic and
// GTK-free, which is what makes the theme testable.
func Render(cfg config.Config) string {
	var b strings.Builder
	// tmpl is parsed at init and the data has no failing cases, so an error
	// here is impossible short of a programming mistake caught by the tests.
	if err := tmpl.Execute(&b, ParamsFor(cfg)); err != nil {
		return ""
	}
	return b.String()
}

var tmpl = template.Must(template.New("banshee.css").Funcs(template.FuncMap{
	// alpha renders an rgba() literal from an "r, g, b" list plus an alpha,
	// keeping the template free of hand-written float formatting.
	"alpha": func(rgb string, a float64) string {
		return "rgba(" + rgb + ", " + formatAlpha(a) + ")"
	},
	"px": func(n int) string { return strconv.Itoa(n) + "px" },
}).Parse(css))

// css is the launcher stylesheet template. Only GTK4-supported properties are
// used; GTK's CSS engine warns (and drops the whole declaration) on anything
// it does not know, so resist copying web-only properties in here.
const css = `
/* banshee launcher — dark glass, {{ .Accent }} accent */

window#banshee-window {
	background-color: transparent;
}

/* The glass panel. Real blur comes from the compositor layerrule; the alpha
   below is tuned so the panel stays readable even without it. */
#banshee-window .panel {
	background-color: rgba({{ .PanelRGB }}, {{ .Opacity }});
	border: 1px solid {{ alpha .AccentRGB 0.35 }};
	border-radius: 14px;
	padding: 10px;
	margin: 4px;
	min-width: {{ px .Width }};
	box-shadow: 0 14px 40px rgba(0, 0, 0, 0.5);
}

/* Query entry */
#banshee-window entry.query {
	background-color: rgba(255, 255, 255, 0.04);
	background-image: none;
	border: 1px solid {{ alpha .AccentRGB 0.22 }};
	border-radius: 10px;
	padding: 8px 12px;
	margin-bottom: 8px;
	min-height: 28px;
	color: #e6e9f0;
	font-size: 15px;
	caret-color: {{ .Accent }};
	box-shadow: none;
}

#banshee-window entry.query:focus,
#banshee-window entry.query:focus-within {
	border-color: {{ alpha .AccentRGB 0.6 }};
	background-color: rgba(255, 255, 255, 0.06);
}

#banshee-window entry.query > text > placeholder {
	color: rgba(230, 233, 240, 0.32);
}

#banshee-window entry.query > text > selection {
	background-color: {{ alpha .AccentRGB 0.35 }};
	color: #ffffff;
}

/* Results list */
#banshee-window scrolledwindow.results-scroll,
#banshee-window scrolledwindow.results-scroll > viewport,
#banshee-window list.results {
	background-color: transparent;
}

#banshee-window scrolledwindow.results-scroll undershoot.top,
#banshee-window scrolledwindow.results-scroll undershoot.bottom {
	background-image: none;
}

#banshee-window list.results > row {
	background-color: transparent;
	/* Reserve the selection bar's width on every row so selecting one does
	   not shift its contents sideways. */
	border-left: 3px solid transparent;
	border-radius: 8px;
	padding: 6px 10px;
	margin: 1px 0;
	transition: background-color 90ms ease-out;
}

#banshee-window list.results > row:hover {
	background-color: rgba(255, 255, 255, 0.045);
}

#banshee-window list.results > row:selected,
#banshee-window list.results > row:selected:hover {
	background-color: {{ alpha .AccentRGB 0.16 }};
	border-left: 3px solid {{ .Accent }};
}

#banshee-window list.results > row:selected .result-title {
	color: #f2f5fb;
}

#banshee-window list.results > row:selected .result-subtitle {
	color: rgba(242, 245, 251, 0.6);
}

/* Row contents */
#banshee-window .result-icon {
	-gtk-icon-size: 24px;
	margin-right: 10px;
}

#banshee-window .result-title {
	color: #dfe4ee;
	font-size: 14px;
}

#banshee-window .result-subtitle {
	color: rgba(223, 228, 238, 0.42);
	font-size: 11px;
}

#banshee-window .result-badge {
	color: {{ .Accent }};
	background-color: {{ alpha .AccentRGB 0.12 }};
	border-radius: 999px;
	padding: 2px 9px;
	margin-left: 10px;
	font-size: 10px;
	font-weight: bold;
}

/* Placeholder shown when a query matches nothing */
#banshee-window .empty {
	color: rgba(223, 228, 238, 0.35);
	font-size: 13px;
	padding: 22px;
}
`
