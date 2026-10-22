package ui

import (
	"strconv"

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
	// rows hold each field's label and widget, parallel to inputs, so a
	// ShowWhen-hidden field disappears as one unit.
	rows []*gtk.Box

	// submit and back exist only when form.SubmitLabel is set.
	submit, back *gtk.Button
}

// formInput is one field's widget, seen only through what the form actually
// asks of it. It exists so a field can be an entry, dropdown, slider or
// checkbox without values/focusFirst/markError growing a type switch, and it
// keeps the inputs slice index-parallel to form.Fields — which is the contract
// FirstMissingRequired's returned index relies on.
type formInput interface {
	// value is the field's current text, exactly as it will be submitted.
	value() string
	// markError flags the field as failing validation.
	markError()
	// grabFocus puts the keyboard on the field.
	grabFocus()
	// onChange registers f to run whenever the field's value changes.
	onChange(f func())
}

// entryInput is a free-text field, masked when the FormField is Secret.
type entryInput struct{ e *gtk.Entry }

func (i *entryInput) value() string { return i.e.Text() }

func (i *entryInput) markError() {
	i.e.AddCSSClass("error")
	i.e.GrabFocus()
}

func (i *entryInput) grabFocus() { i.e.GrabFocus() }

func (i *entryInput) onChange(f func()) { i.e.ConnectChanged(f) }

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

func (i *dropdownInput) onChange(f func()) { i.d.NotifyProperty("selected", f) }

// sliderInput is an integer range field (FormField.Slider). spec is the
// normalized spec, kept so every read snaps to the same grid the field
// declared.
type sliderInput struct {
	s    *gtk.Scale
	spec providers.SliderSpec
}

func (i *sliderInput) value() string {
	return strconv.Itoa(SnapSlider(i.spec, i.s.Value()))
}

// markError does nothing: a slider always holds a value in range.
func (i *sliderInput) markError() {}

func (i *sliderInput) grabFocus() { i.s.GrabFocus() }

func (i *sliderInput) onChange(f func()) { i.s.ConnectValueChanged(f) }

// checkboxInput is a boolean field (FormField.Checkbox) submitting "true" or
// "false". The check button carries the field label itself, so clicking the
// label toggles it.
type checkboxInput struct{ c *gtk.CheckButton }

func (i *checkboxInput) value() string { return strconv.FormatBool(i.c.Active()) }

// markError does nothing: both states of a checkbox are valid.
func (i *checkboxInput) markError() {}

func (i *checkboxInput) grabFocus() { i.c.GrabFocus() }

func (i *checkboxInput) onChange(f func()) { i.c.ConnectToggled(f) }

// newFormView builds the widget tree for res.Form. res.Form must be non-nil.
//
// With SubmitLabel set, Enter goes to GTK: a focused check button may toggle
// and a scale ignores it, so only the button submits. Without it, Enter
// submits from any field and Space toggles a checkbox.
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
		row := gtk.NewBox(gtk.OrientationVertical, 0)
		row.AddCSSClass("form-row")
		v.root.Append(row)
		v.rows = append(v.rows, row)

		kind := KindOf(f)
		if kind != KindCheckbox {
			// A checkbox labels itself; a separate label would not toggle it.
			label := gtk.NewLabel(f.Label)
			label.AddCSSClass("form-label")
			label.SetXAlign(0)
			row.Append(label)
		}

		switch kind {
		case KindDropdown:
			v.addDropdown(row, f)
		case KindSlider:
			v.addSlider(row, f)
		case KindCheckbox:
			v.addCheckbox(row, f)
		default:
			v.addEntry(row, f)
		}
	}

	// Connected only now: construction-time SetValue/SetActive would fire
	// refreshVisibility while rows and inputs are still incomplete.
	for _, in := range v.inputs {
		in.onChange(v.refreshVisibility)
	}
	v.refreshVisibility()

	if v.form.SubmitLabel != "" {
		v.addButtons()
		return v
	}

	hint := gtk.NewLabel("Enter to save · Esc to go back")
	hint.AddCSSClass("form-hint")
	hint.SetXAlign(0)
	v.root.Append(hint)

	return v
}

// refreshVisibility shows exactly the rows whose ShowWhen condition holds for
// the current values. The form stack page is vertically homogeneous, so
// hiding rows never resizes the panel.
func (v *formView) refreshVisibility() {
	vis := Visible(v.form.Fields, TrimValues(v.values()))
	for i, row := range v.rows {
		row.SetVisible(vis[i])
	}
}

// addButtons appends the submit button and the Back button below the fields.
func (v *formView) addButtons() {
	v.submit = gtk.NewButtonWithLabel(v.form.SubmitLabel)
	v.submit.AddCSSClass("form-submit")
	v.submit.SetHExpand(true)
	v.root.Append(v.submit)

	v.back = gtk.NewButtonWithLabel("Back")
	v.back.AddCSSClass("form-back")
	v.back.SetHExpand(true)
	v.root.Append(v.back)
}

// connectButtons wires the submit and Back buttons; a form without them
// ignores the call.
func (v *formView) connectButtons(onSubmit, onBack func()) {
	if v.submit != nil {
		v.submit.ConnectClicked(onSubmit)
	}
	if v.back != nil {
		v.back.ConnectClicked(onBack)
	}
}

// addEntry appends f's text entry to row and registers it as an input.
func (v *formView) addEntry(row *gtk.Box, f providers.FormField) {
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
	row.Append(entry)
	v.inputs = append(v.inputs, &entryInput{e: entry})
}

// addDropdown appends f's fixed-choice dropdown to row and registers it as an
// input. GtkDropDown preselects index 0, which is the contract's "first
// option preselected".
func (v *formView) addDropdown(row *gtk.Box, f providers.FormField) {
	opts := make([]string, len(f.Options))
	copy(opts, f.Options) // the field is the provider's; the widget outlives the query
	d := gtk.NewDropDownFromStrings(opts)
	d.AddCSSClass("form-field")
	d.SetHExpand(true)
	row.Append(d)
	v.inputs = append(v.inputs, &dropdownInput{d: d, options: opts})
}

// addSlider appends f's integer slider to row and registers it as an input.
// KindOf has already rejected a spec with no range.
func (v *formView) addSlider(row *gtk.Box, f providers.FormField) {
	spec, _ := NormalizeSlider(*f.Slider)
	s := gtk.NewScaleWithRange(gtk.OrientationHorizontal,
		float64(spec.Min), float64(spec.Max), float64(spec.Step))
	s.SetDrawValue(true)
	s.SetDigits(0)
	s.SetValuePos(gtk.PosRight)
	s.SetHExpand(true)
	s.SetValue(float64(spec.Default))
	s.AddCSSClass("form-field")
	// GtkRange does not snap to its step while dragging, so the handle is
	// pulled back onto the grid; the re-entrant SetValue settles in one round.
	s.ConnectValueChanged(func() {
		if snapped := float64(SnapSlider(spec, s.Value())); snapped != s.Value() {
			s.SetValue(snapped)
		}
	})
	row.Append(s)
	v.inputs = append(v.inputs, &sliderInput{s: s, spec: spec})
}

// addCheckbox appends f's check button, labelled with f.Label, to row and
// registers it as an input.
func (v *formView) addCheckbox(row *gtk.Box, f providers.FormField) {
	c := gtk.NewCheckButtonWithLabel(f.Label)
	c.SetActive(f.Checked)
	c.AddCSSClass("form-field")
	row.Append(c)
	v.inputs = append(v.inputs, &checkboxInput{c: c})
}

// values returns the current field values keyed by FormField.Key.
func (v *formView) values() map[string]string {
	out := make(map[string]string, len(v.inputs))
	for i, in := range v.inputs {
		out[v.form.Fields[i].Key] = in.value()
	}
	return out
}

// focusFirst puts the keyboard on the first visible field.
func (v *formView) focusFirst() {
	for i, in := range v.inputs {
		if v.rows[i].Visible() {
			in.grabFocus()
			return
		}
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
