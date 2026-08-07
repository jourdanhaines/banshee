package ui

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"

	"github.com/jourdanhaines/banshee/internal/providers"
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

// newIconWidget resolves a result's icon to a 24px image. Every branch falls
// back to a blank image of the same size rather than to nothing, so titles
// stay aligned in a list that mixes iconed and icon-less results.
func (l *Launcher) newIconWidget(ic providers.Icon) gtk.Widgetter {
	var img *gtk.Image

	switch kind, value := ResolveIcon(ic); kind {
	case IconApp:
		if info := l.appInfo(value); info != nil {
			if gicon := info.Icon(); gicon != nil {
				img = gtk.NewImageFromGIcon(gicon)
			}
		}
		if img == nil {
			// Most desktop IDs double as icon names ("org.gnome.Nautilus");
			// try that before giving up.
			img = gtk.NewImageFromIconName(strings.TrimSuffix(desktopID(value), ".desktop"))
		}
	case IconTheme:
		img = gtk.NewImageFromIconName(value)
	case IconFile:
		img = gtk.NewImageFromFile(value)
	}

	if img == nil {
		img = gtk.NewImage()
	}
	img.SetPixelSize(iconPixelSize)
	img.SetVAlign(gtk.AlignCenter)
	img.AddCSSClass("result-icon")
	return img
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
