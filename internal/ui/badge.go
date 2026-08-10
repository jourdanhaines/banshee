package ui

import (
	"html"
	"strings"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/theme"
)

// CategoryLabel returns the short text shown in a row's right-aligned badge.
// Unknown categories (a provider registered against a newer Category constant)
// get no badge rather than a misleading one — forward compatibility applies to
// enums too.
func CategoryLabel(c providers.Category) string {
	switch c {
	case providers.CatSession:
		return "session"
	case providers.CatGitHub:
		return "github"
	case providers.CatConnector:
		return "connector"
	case providers.CatDirectory:
		return "directory"
	case providers.CatClipboard:
		return "clipboard"
	case providers.CatApp:
		return "app"
	case providers.CatKill:
		return "kill"
	case providers.CatPlugin:
		return "plugin"
	default:
		return ""
	}
}

// badgeMarkup returns pango markup colouring text with a per-result accent, or
// "" when the badge should just use the stylesheet's .result-badge colour.
//
// Result.Accent comes from third-party plugin and connector manifests, so it
// is validated as a hex colour before being interpolated: an unvalidated value
// would let a manifest inject arbitrary pango markup into every row. The text
// itself is escaped for the same reason.
func badgeMarkup(text, accent string) string {
	c, ok := theme.ParseHexColor(accent)
	if !ok || strings.TrimSpace(text) == "" {
		return ""
	}
	return `<span foreground="` + c.Hex() + `">` + html.EscapeString(text) + `</span>`
}
