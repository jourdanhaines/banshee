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
	defaultOpacity = 0.92
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

/* Query entry and form fields share one look */
#banshee-window entry.form-field,
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

#banshee-window entry.form-field:focus,
#banshee-window entry.form-field:focus-within,
#banshee-window entry.query:focus,
#banshee-window entry.query:focus-within {
	border-color: {{ alpha .AccentRGB 0.6 }};
	background-color: rgba(255, 255, 255, 0.06);
}

#banshee-window entry.form-field > text > placeholder,
#banshee-window entry.query > text > placeholder {
	color: rgba(230, 233, 240, 0.32);
}

#banshee-window entry.form-field > text > selection,
#banshee-window entry.query > text > selection {
	background-color: {{ alpha .AccentRGB 0.35 }};
	color: #ffffff;
}

/* Form view (secondary input slid in over the results) */
#banshee-window .form-view {
	padding: 4px 2px;
}

#banshee-window .form-title {
	color: #f2f5fb;
	font-size: 15px;
	font-weight: bold;
	margin-bottom: 10px;
}

#banshee-window .form-label {
	color: rgba(223, 228, 238, 0.55);
	font-size: 12px;
	margin-bottom: 4px;
}

#banshee-window entry.form-field.error {
	border-color: rgba(247, 118, 142, 0.7);
}

#banshee-window .form-hint {
	color: rgba(223, 228, 238, 0.35);
	font-size: 11px;
	margin-top: 10px;
}

/* A fixed-choice form field (FormField.Options). GtkDropDown renders as a
   button plus a popover, so the button carries the entry look and the popover
   is themed separately — it is its own GtkNative and inherits nothing from the
   panel. */
#banshee-window dropdown.form-field {
	margin-bottom: 8px;
}

#banshee-window dropdown.form-field > button {
	background-color: rgba(255, 255, 255, 0.04);
	background-image: none;
	border: 1px solid {{ alpha .AccentRGB 0.22 }};
	border-radius: 10px;
	padding: 8px 12px;
	min-height: 28px;
	color: #e6e9f0;
	font-size: 15px;
	box-shadow: none;
}

#banshee-window dropdown.form-field > button:focus,
#banshee-window dropdown.form-field > button:focus-within,
#banshee-window dropdown.form-field > button:hover {
	border-color: {{ alpha .AccentRGB 0.6 }};
	background-color: rgba(255, 255, 255, 0.06);
}

#banshee-window dropdown.form-field > popover > contents {
	background-color: rgba({{ .PanelRGB }}, 0.98);
	border: 1px solid {{ alpha .AccentRGB 0.35 }};
	border-radius: 10px;
	padding: 4px;
	box-shadow: 0 8px 24px rgba(0, 0, 0, 0.45);
}

#banshee-window dropdown.form-field > popover listview > row {
	border-radius: 8px;
	padding: 6px 10px;
	color: #dfe4ee;
	font-size: 14px;
}

#banshee-window dropdown.form-field > popover listview > row:selected {
	background-color: {{ alpha .AccentRGB 0.16 }};
	color: #f2f5fb;
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
}

#banshee-window list.results > row:selected .result-title {
	color: #f2f5fb;
}

#banshee-window list.results > row:selected .result-subtitle {
	color: rgba(242, 245, 251, 0.6);
}

/* Row contents. The color below tints symbolic theme icons to the accent;
   full-color app icons and pre-tinted builtin SVGs ignore it. */
#banshee-window .result-icon {
	-gtk-icon-size: 24px;
	margin-right: 10px;
	color: {{ .Accent }};
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

/* TOTP drain bars. The shared bar sits under the query entry and stands in for
   every standard-window code on screen; .row-timer is the thinner per-row
   variant a non-standard period gets. GtkProgressBar draws through
   progressbar > trough > progress, so both nodes need styling. */
#banshee-window progressbar.code-timer > trough {
	background-color: rgba(255, 255, 255, 0.06);
	border: none;
	border-radius: 999px;
	min-height: 3px;
}

#banshee-window progressbar.code-timer > trough > progress {
	background-color: {{ .Accent }};
	background-image: none;
	border: none;
	border-radius: 999px;
	min-height: 3px;
}

#banshee-window progressbar.code-timer {
	margin: 0 2px 8px 2px;
}

#banshee-window progressbar.row-timer {
	margin: 3px 0 0 0;
}

#banshee-window progressbar.row-timer > trough {
	background-color: rgba(255, 255, 255, 0.04);
	min-height: 2px;
}

#banshee-window progressbar.row-timer > trough > progress {
	background-color: {{ alpha .AccentRGB 0.75 }};
	min-height: 2px;
}

/* Placeholder shown when a query matches nothing */
#banshee-window .empty {
	color: rgba(223, 228, 238, 0.35);
	font-size: 13px;
	padding: 22px;
}
`
