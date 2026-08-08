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

	root   *gtk.Box
	inputs []formInput // parallel to form.Fields
}

// formInput is one field's widget, seen only through what the form actually
// asks of it. It exists so a field can be a text entry or a fixed-choice
// dropdown without values/focusFirst/markError growing a type switch, and it
// keeps the inputs slice index-parallel to form.Fields — which is the contract
// FirstMissingRequired's returned index relies on.
type formInput interface {
	// value is the field's current text, exactly as it will be submitted.
	value() string
	// markError flags the field as failing validation.
	markError()
	// grabFocus puts the keyboard on the field.
	grabFocus()
}

// entryInput is a free-text field, masked when the FormField is Secret.
type entryInput struct{ e *gtk.Entry }

func (i *entryInput) value() string { return i.e.Text() }

func (i *entryInput) markError() {
	i.e.AddCSSClass("error")
	i.e.GrabFocus()
}

func (i *entryInput) grabFocus() { i.e.GrabFocus() }

// dropdownInput is a fixed-choice field (FormField.Options). The option
// strings are kept alongside the widget because GtkDropDown reports a
// selection as an index into the model it was built from, and the submitted
// value must be the option string itself.
type dropdownInput struct {
	d       *gtk.DropDown
	options []string
}

// value is the selected option. It cannot be empty for a real selection —
// which is what lets FirstMissingRequired skip option fields entirely — and
// falls back to "" only for GTK's "nothing selected" sentinel, a state a
// dropdown built from a non-empty list should never reach.
func (i *dropdownInput) value() string {
	sel := i.d.Selected()
	if sel == gtk.InvalidListPosition || int(sel) >= len(i.options) {
		return ""
	}
	return i.options[sel]
}

// markError does nothing: a dropdown always carries one of its own options, so
// there is no invalid state to point the user at.
func (i *dropdownInput) markError() {}

func (i *dropdownInput) grabFocus() { i.d.GrabFocus() }

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

	for _, f := range v.form.Fields {
		label := gtk.NewLabel(f.Label)
		label.AddCSSClass("form-label")
		label.SetXAlign(0)
		v.root.Append(label)

		// Non-emptiness, not nil-ness, is the contract: a plugin may legally
		// send "options": [], which decodes to an empty non-nil slice and must
		// still render as free text.
		if len(f.Options) > 0 {
			v.addDropdown(f)
			continue
		}
		v.addEntry(f)
	}

	hint := gtk.NewLabel("Enter to save · Esc to go back")
	hint.AddCSSClass("form-hint")
	hint.SetXAlign(0)
	v.root.Append(hint)

	return v
}

// addEntry appends f's text entry to the form and registers it as an input.
func (v *formView) addEntry(f providers.FormField) {
	entry := gtk.NewEntry()
	entry.AddCSSClass("form-field")
	entry.SetPlaceholderText(f.Placeholder)
	entry.SetHExpand(true)
	if f.Secret {
		// A plain masked Entry, not gtk.PasswordEntry: it keeps every field
		// behind the same formInput seam and inherits the shared .form-field
		// styling.
		entry.SetVisibility(false)
		entry.SetInputPurpose(gtk.InputPurposePassword)
		entry.AddCSSClass("secret")
	}
	// Typing clears a validation error the moment the field changes.
	entry.ConnectChanged(func() { entry.RemoveCSSClass("error") })
	v.root.Append(entry)
	v.inputs = append(v.inputs, &entryInput{e: entry})
}

// addDropdown appends f's fixed-choice dropdown to the form and registers it as
// an input. GtkDropDown preselects index 0, which is the contract's "first
// option preselected".
func (v *formView) addDropdown(f providers.FormField) {
	opts := make([]string, len(f.Options))
	copy(opts, f.Options) // the field is the provider's; the widget outlives the query
	d := gtk.NewDropDownFromStrings(opts)
	d.AddCSSClass("form-field")
	d.SetHExpand(true)
	v.root.Append(d)
	v.inputs = append(v.inputs, &dropdownInput{d: d, options: opts})
}

// values returns the current field values keyed by FormField.Key.
func (v *formView) values() map[string]string {
	out := make(map[string]string, len(v.inputs))
	for i, in := range v.inputs {
		out[v.form.Fields[i].Key] = in.value()
	}
	return out
}

// focusFirst puts the keyboard on the first field.
func (v *formView) focusFirst() {
	if len(v.inputs) > 0 {
		v.inputs[0].grabFocus()
	}
}

// markError highlights field i as failing validation and focuses it.
func (v *formView) markError(i int) {
	if i < 0 || i >= len(v.inputs) {
		return
	}
	v.inputs[i].markError()
}

// dropdownListOpen reports whether any of this form's dropdowns currently has
// its option list popped up. The key controller consults it before submitting
// on Enter: with the list open, Enter belongs to the widget (it commits the
// highlighted option), and swallowing it as a submit would make a fixed-choice
// field unusable from the keyboard.
//
// The popped-up list is the whole predicate, deliberately: a dropdown that
// merely *holds focus* with its list closed must not swallow Enter. A dropdown
// is often the form's last field (the add form's "Storage"), so treating focus
// as ownership left Enter reopening the list forever — with no submit button
// and no mouse path, submitForm became unreachable and the form's own
// "Enter to save" hint was a lie. Committing an option pops the list back down,
// so the next Enter submits, which is the sequence the hint promises.
func (v *formView) dropdownListOpen() bool {
	if v == nil {
		return false
	}
	for _, in := range v.inputs {
		if d, ok := in.(*dropdownInput); ok && d.popoverVisible() {
			return true
		}
	}
	return false
}

// popoverVisible reports whether the dropdown's option list is popped up. The
// popover has no getter on GtkDropDown, so it is found among the widget's own
// children — GTK4 parents a popover to the widget it belongs to.
func (i *dropdownInput) popoverVisible() bool {
	for c := gtk.BaseWidget(i.d).FirstChild(); c != nil; c = gtk.BaseWidget(c).NextSibling() {
		if p, ok := c.(*gtk.Popover); ok && p.Visible() {
			return true
		}
	}
	return false
}
