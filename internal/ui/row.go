package ui

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gdkpixbuf/v2"
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
//	   │  ├─ Label .result-title             ellipsized
//	   │  ├─ Label .result-subtitle          ellipsized, optional
//	   │  └─ ProgressBar .code-timer.row-timer  optional, non-standard periods
//	   └─ Label .result-badge                right-aligned, optional
//
// A Result.Preview that resolves to a decodable image grows the row a second
// storey — the horizontal line above becomes the header of a vertical box
// with a large Picture .result-preview below it. The panel height never
// changes (min == max resultsHeight is on the scroller); a tall row just
// occupies more of the scrolled range.
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

	// A live row on a non-standard window drains a bar of its own: the shared
	// bar under the query speaks only for the standard unix%30 window, and a
	// 60-second entry would be misrepresented by it.
	if !r.Expiry.IsZero() && !IsStandard(r.Period) {
		bar := gtk.NewProgressBar()
		bar.AddCSSClass("code-timer")
		bar.AddCSSClass("row-timer")
		bar.SetFraction(Fraction(r.Expiry, r.Period, time.Now()))
		l.liveRows = append(l.liveRows, liveRow{bar: bar, expiry: r.Expiry, period: r.Period})
		text.Append(bar)
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

	if pic := l.previewPicture(r.Preview); pic != nil {
		outer := gtk.NewBox(gtk.OrientationVertical, 0)
		outer.Append(box)
		outer.Append(pic)
		row.SetChild(outer)
	} else {
		row.SetChild(box)
	}
	return row
}

// previewMaxHeight caps a preview image's rendered height. It must sit
// comfortably under resultsHeight (420): scrollToRow clamps the viewport to
// the selected row's extent, and a row taller than the viewport could never
// be brought fully into view — 240 keeps the selected preview plus at least
// one neighbouring row visible.
const previewMaxHeight = 240

// previewPicture renders a Result.Preview path as the large lower-storey
// image, or nil when the row should stay single-storey: no preview, a
// non-absolute path (same rule ResolveIcon applies to Icon.Path), or a file
// that is gone or undecodable — never GTK's broken-image placeholder, the
// same contract newIconWidget keeps.
func (l *Launcher) previewPicture(path string) *gtk.Picture {
	if path == "" || !strings.HasPrefix(path, "/") {
		return nil
	}
	tex := l.previewTexture(path)
	if tex == nil {
		return nil
	}
	pic := gtk.NewPictureForPaintable(tex)
	// ScaleDown + CanShrink: an over-wide texture shrinks to the row's width
	// keeping its aspect, a small one renders at natural size — never
	// upscaled into a blur. The height cap is baked into the texture itself
	// (previewTexture downscales at decode time) because GTK4 CSS has no
	// max-height and a Picture's natural request would otherwise set the row
	// height.
	pic.SetContentFit(gtk.ContentFitScaleDown)
	pic.SetCanShrink(true)
	pic.SetHAlign(gtk.AlignStart)
	pic.AddCSSClass("result-preview")
	return pic
}

// previewTexture decodes a preview image, downscaled to previewMaxHeight when
// taller, and caches the result by path. Rows are rebuilt on every keystroke;
// decoding a full-size clipboard PNG per keystroke would be far more visible
// than the SVG case builtinTexture exists for. Cached textures are already
// capped, so the cache stays small; Hide and Reload drop it because the
// backing files (clipboard-history tmpfs payloads) can be deleted behind it.
func (l *Launcher) previewTexture(path string) *gdk.Texture {
	if tex, ok := l.previews[path]; ok {
		return tex
	}
	var tex *gdk.Texture
	if pb, err := gdkpixbuf.NewPixbufFromFile(path); err == nil {
		if h := pb.Height(); h > previewMaxHeight && h > 0 {
			w := pb.Width() * previewMaxHeight / h
			if w < 1 {
				w = 1
			}
			if scaled := pb.ScaleSimple(w, previewMaxHeight, gdkpixbuf.InterpBilinear); scaled != nil {
				pb = scaled
			}
		}
		tex = gdk.NewTextureForPixbuf(pb)
	} else {
		log.Printf("ui: preview %q failed to decode: %v", path, err)
	}
	if l.previews == nil {
		l.previews = make(map[string]*gdk.Texture)
	}
	l.previews[path] = tex // negative results cached too — no retry per row
	return tex
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
