package ui

import (
	"strings"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// IconKind names the widget-construction strategy for a providers.Icon. The
// decision is separated from the GTK call so it can be unit tested; see
// newIconWidget for the (untestable) GTK half.
type IconKind int

const (
	// IconNone means the result has no icon and the row should reserve blank
	// space so titles stay aligned.
	IconNone IconKind = iota
	// IconApp resolves a .desktop ID through gio and uses the app's GIcon.
	IconApp
	// IconBuiltin renders a compiled-in SVG (internal/icons) tinted with the
	// theme accent.
	IconBuiltin
	// IconTheme looks the name up in the current icon theme.
	IconTheme
	// IconFile loads an image file from an absolute path.
	IconFile
)

// String implements fmt.Stringer for readable test failures and logs.
func (k IconKind) String() string {
	switch k {
	case IconApp:
		return "app"
	case IconBuiltin:
		return "builtin"
	case IconTheme:
		return "theme"
	case IconFile:
		return "file"
	default:
		return "none"
	}
}

// ResolveIcon picks the single strategy used to build a row's icon widget and
// returns the value that strategy needs.
//
// providers.Icon documents that exactly one field should be set, but providers
// are third-party code (exec plugins fill this struct from JSON), so the
// precedence is fixed and documented rather than left to chance: AppID, then
// Builtin, then ThemeName, then Path. Whitespace-only fields count as unset,
// and a Path that is not absolute is rejected — a relative path would resolve
// against the daemon's working directory, which is meaningless to a plugin
// author.
func ResolveIcon(ic providers.Icon) (IconKind, string) {
	if v := strings.TrimSpace(ic.AppID); v != "" {
		return IconApp, v
	}
	if v := strings.TrimSpace(ic.Builtin); v != "" {
		return IconBuiltin, v
	}
	if v := strings.TrimSpace(ic.ThemeName); v != "" {
		return IconTheme, v
	}
	if v := strings.TrimSpace(ic.Path); v != "" && strings.HasPrefix(v, "/") {
		return IconFile, v
	}
	return IconNone, ""
}

// desktopID normalizes a .desktop application ID for comparison against
// gio.AppInfo.ID(), which always carries the ".desktop" suffix. Providers may
// supply either form.
func desktopID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if !strings.HasSuffix(id, ".desktop") {
		id += ".desktop"
	}
	return id
}
