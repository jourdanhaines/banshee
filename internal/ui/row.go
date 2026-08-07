package ui

import (
	"log"
	"os"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/jourdanhaines/banshee/internal/icons"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/theme"
)

// newRow builds one result row:
//
//	ListBoxRow
//	└─ Box (horizontal)
//	   ├─ Image .result-icon        24px
//	   ├─ Box (vertical, hexpand)
//	   │  ├─ Label .result-title    ellipsized
//	   │  └─ Label .result-subtitle ellipsized, optional
//	   └─ Label .result-badge       right-aligned, optional
func (l *Launcher) newRow(r providers.Result) *gtk.ListBoxRow {
	row := gtk.NewListBoxRow()

	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.Append(l.newIconWidget(r.Icon))

	text := gtk.NewBox(gtk.OrientationVertical, 0)
	text.SetHExpand(true)
	text.SetVAlign(gtk.AlignCenter)

	title := newEllipsizedLabel(r.Title)
	title.AddCSSClass("result-title")
	text.Append(title)

	if r.Subtitle != "" {
		sub := newEllipsizedLabel(r.Subtitle)
		sub.AddCSSClass("result-subtitle")
		text.Append(sub)
	}
	box.Append(text)

	if label := CategoryLabel(r.Category); label != "" {
		badge := gtk.NewLabel(label)
		badge.AddCSSClass("result-badge")
		badge.SetHAlign(gtk.AlignEnd)
		badge.SetVAlign(gtk.AlignCenter)
		// A connector or plugin may carry its own accent; when it is a valid
		// hex colour it overrides the stylesheet's default badge colour.
		if m := badgeMarkup(label, r.Accent); m != "" {
			badge.SetMarkup(m)
		}
		box.Append(badge)
	}

	row.SetChild(box)
	return row
}

// newEllipsizedLabel returns a left-aligned label that truncates rather than
// widening its parent. max-width-chars caps the *natural* width request while
// hexpand lets the label fill whatever the panel actually offers — the
// standard GTK idiom for "ellipsize to fit".
func newEllipsizedLabel(s string) *gtk.Label {
	lbl := gtk.NewLabel(s)
	lbl.SetXAlign(0)
	lbl.SetHExpand(true)
	lbl.SetEllipsize(pango.EllipsizeEnd)
	lbl.SetMaxWidthChars(1)
	lbl.SetSingleLineMode(true)
	return lbl
}

// fallbackIconName is shown when a result's icon cannot be resolved — a
// themed generic instead of GTK's white "image-missing" sheet, and symbolic
// so the accent tint applies like every other themed icon.
const fallbackIconName = "application-x-executable-symbolic"

// newIconWidget resolves a result's icon to a 24px image. Every branch that
// cannot produce a real icon falls through to fallbackIconName, so rows never
// show the broken-image placeholder and titles stay aligned.
func (l *Launcher) newIconWidget(ic providers.Icon) gtk.Widgetter {
	var img *gtk.Image

	switch kind, value := ResolveIcon(ic); kind {
	case IconApp:
		if info := l.appInfo(value); info != nil {
			if gicon := info.Icon(); gicon != nil && giconRenderable(gicon) {
				img = gtk.NewImageFromGIcon(gicon)
			}
		}
		if img == nil {
			// Most desktop IDs double as icon names ("org.gnome.Nautilus");
			// try that before giving up.
			if name := strings.TrimSuffix(desktopID(value), ".desktop"); themeHasIcon(name) {
				img = gtk.NewImageFromIconName(name)
			}
		}
	case IconBuiltin:
		if tex := l.builtinTexture(value); tex != nil {
			img = gtk.NewImageFromPaintable(tex)
		}
	case IconTheme:
		if themeHasIcon(value) {
			img = gtk.NewImageFromIconName(value)
		}
	case IconFile:
		if _, err := os.Stat(value); err == nil {
			img = gtk.NewImageFromFile(value)
		}
	}

	if img == nil {
		img = gtk.NewImageFromIconName(fallbackIconName)
	}
	img.SetPixelSize(iconPixelSize)
	img.SetVAlign(gtk.AlignCenter)
	img.AddCSSClass("result-icon")
	return img
}

// iconTheme returns the display's icon theme, nil when headless.
func iconTheme() *gtk.IconTheme {
	d := gdk.DisplayGetDefault()
	if d == nil {
		return nil
	}
	return gtk.IconThemeGetForDisplay(d)
}

// themeHasIcon reports whether the icon theme can render name. With no
// display (tests) it optimistically says yes — there is nothing to render
// anyway.
func themeHasIcon(name string) bool {
	th := iconTheme()
	return th == nil || th.HasIcon(name)
}

// giconRenderable reports whether a .desktop file's GIcon will actually
// render. Themed icons are checked against the icon theme (a missing name is
// exactly the white broken-image case); file-backed and other icon types are
// trusted — the theme knows nothing about them.
func giconRenderable(gicon *gio.Icon) bool {
	if _, themed := gicon.Cast().(*gio.ThemedIcon); !themed {
		return true
	}
	th := iconTheme()
	return th == nil || th.HasGIcon(gicon)
}

// builtinTexture renders a compiled-in SVG icon tinted with the theme accent.
// Decoding an SVG per row per keystroke would be visible, so textures are
// cached; the accent is baked into the pixels, and Reload drops the cache
// when the theme may have changed. Returns nil when the icon is unknown or
// the pixbuf loader cannot decode SVG — the caller falls back to blank.
func (l *Launcher) builtinTexture(name string) *gdk.Texture {
	if tex, ok := l.builtins[name]; ok {
		return tex
	}
	var tex *gdk.Texture
	if data, ok := icons.SVG(name, theme.ParamsFor(l.cfg).Accent); ok {
		t, err := gdk.NewTextureFromBytes(glib.NewBytesWithGo(data))
		if err != nil {
			log.Printf("ui: builtin icon %q failed to decode: %v", name, err)
		} else {
			tex = t
		}
	}
	if l.builtins == nil {
		l.builtins = make(map[string]*gdk.Texture)
	}
	l.builtins[name] = tex // negative results cached too — no retry per row
	return tex
}

// appInfo looks a .desktop ID up in gio's application list. The list is built
// once and cached: scanning every .desktop file on the system per row would be
// visible at 30 rows per keystroke. Reload drops the cache.
func (l *Launcher) appInfo(id string) *gio.AppInfo {
	id = desktopID(id)
	if id == "" {
		return nil
	}
	if l.apps == nil {
		l.apps = make(map[string]*gio.AppInfo)
		for _, info := range gio.AppInfoGetAll() {
			if info == nil {
				continue
			}
			if key := info.ID(); key != "" {
				l.apps[key] = info
			}
		}
	}
	return l.apps[id]
}
