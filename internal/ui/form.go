package ui

import (
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// formView is the widget half of an open form. All decisions that need no
// display (validation, value trimming) live in formstate.go; this file is
// plumbing only, per the package's GTK/logic split.
type formView struct {
	// res is a copy of the activated result. The launcher's result slice can
	// be replaced by a late query generation while the form is open, so the
	// form must never index back into it.
	res  providers.Result
	form providers.Form

	root    *gtk.Box
	entries []*gtk.Entry // parallel to form.Fields
}

// newFormView builds the widget tree for res.Form. res.Form must be non-nil.
func newFormView(res providers.Result) *formView {
	v := &formView{res: res, form: *res.Form}

	v.root = gtk.NewBox(gtk.OrientationVertical, 0)
	v.root.AddCSSClass("form-view")

	title := gtk.NewLabel(v.form.Title)
	title.AddCSSClass("form-title")
	title.SetXAlign(0)
	title.SetEllipsize(2) // PANGO_ELLIPSIZE_MIDDLE keeps long repo paths readable
	v.root.Append(title)

	for i, f := range v.form.Fields {
		label := gtk.NewLabel(f.Label)
		label.AddCSSClass("form-label")
		label.SetXAlign(0)
		v.root.Append(label)

		entry := gtk.NewEntry()
		entry.AddCSSClass("form-field")
		entry.SetPlaceholderText(f.Placeholder)
		entry.SetHExpand(true)
		if f.Secret {
			// A plain masked Entry, not gtk.PasswordEntry: it keeps
			// v.entries uniform (values/markError index straight into it)
			// and inherits the shared .form-field styling.
			entry.SetVisibility(false)
			entry.SetInputPurpose(gtk.InputPurposePassword)
			entry.AddCSSClass("secret")
		}
		// Typing clears a validation error the moment the field changes.
		e := entry
		entry.ConnectChanged(func() { e.RemoveCSSClass("error") })
		v.root.Append(entry)
		v.entries = append(v.entries, entry)
		_ = i
	}

	hint := gtk.NewLabel("Enter to save · Esc to go back")
	hint.AddCSSClass("form-hint")
	hint.SetXAlign(0)
	v.root.Append(hint)

	return v
}

// values returns the current entry texts keyed by FormField.Key.
func (v *formView) values() map[string]string {
	out := make(map[string]string, len(v.entries))
	for i, e := range v.entries {
		out[v.form.Fields[i].Key] = e.Text()
	}
	return out
}

// focusFirst puts the keyboard caret in the first field.
func (v *formView) focusFirst() {
	if len(v.entries) > 0 {
		v.entries[0].GrabFocus()
	}
}

// markError highlights field i as failing validation and focuses it.
func (v *formView) markError(i int) {
	if i < 0 || i >= len(v.entries) {
		return
	}
	v.entries[i].AddCSSClass("error")
	v.entries[i].GrabFocus()
}
