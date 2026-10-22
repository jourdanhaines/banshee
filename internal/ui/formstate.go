package ui

import (
	"math"
	"slices"
	"strings"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// TrimValues returns a copy of values with every value TrimSpace'd, so
// providers and validation both see what the user meant rather than stray
// whitespace.
func TrimValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for k, v := range values {
		out[k] = strings.TrimSpace(v)
	}
	return out
}

// FieldKind is the widget a form field renders as, resolved once by KindOf so
// the GTK view and validation agree on a field's precedence.
type FieldKind int

// The field kinds, in the order KindOf prefers them after KindEntry.
const (
	KindEntry FieldKind = iota
	KindDropdown
	KindSlider
	KindCheckbox
)

// KindOf resolves a field's widget kind by precedence Options > Slider >
// Checkbox > entry. A slider whose Max <= Min is ignored, so the field falls
// through to the next kind rather than rendering an unusable range.
func KindOf(f providers.FormField) FieldKind {
	switch {
	case len(f.Options) > 0:
		return KindDropdown
	case f.Slider != nil && f.Slider.Max > f.Slider.Min:
		return KindSlider
	case f.Checkbox:
		return KindCheckbox
	}
	return KindEntry
}

// NormalizeSlider makes a slider spec usable instead of rejecting it: Step is
// forced into [1, Max-Min] and Default is clamped and snapped to the grid.
// ok is false when Max <= Min, which leaves no range to slide over.
func NormalizeSlider(s providers.SliderSpec) (providers.SliderSpec, bool) {
	if s.Max <= s.Min {
		return s, false
	}
	s.Step = max(s.Step, 1)
	s.Step = min(s.Step, s.Max-s.Min)
	s.Default = SnapSlider(s, float64(s.Default))
	return s, true
}

// SnapSlider maps a raw slider position onto the nearest Min+k*Step grid
// point within [Min, Max]; GTK's scale does not snap while dragging. s must
// already be normalized.
func SnapSlider(s providers.SliderSpec, raw float64) int {
	raw = math.Max(float64(s.Min), math.Min(float64(s.Max), raw))
	k := math.Round((raw - float64(s.Min)) / float64(s.Step))
	v := s.Min + int(k)*s.Step
	if v > s.Max {
		// Rounding up past an off-grid Max lands outside the range.
		v -= s.Step
	}
	return v
}

// Visible reports, index-parallel to fields, which fields their ShowWhen
// conditions currently show (AND). Only earlier fields resolve as a
// condition's key (first occurrence wins); an unresolvable condition is
// ignored, and a field hidden itself hides its dependants.
func Visible(fields []providers.FormField, values map[string]string) []bool {
	vis := make([]bool, len(fields))
	seen := make(map[string]int, len(fields))
	for i, f := range fields {
		vis[i] = true
		for _, c := range f.ShowWhen {
			j, ok := seen[c.Key]
			if !ok || len(c.Values) == 0 {
				continue
			}
			if !vis[j] || !slices.Contains(c.Values, values[c.Key]) {
				vis[i] = false
				break
			}
		}
		if _, dup := seen[f.Key]; !dup {
			seen[f.Key] = i
		}
	}
	return vis
}

// VisibleValues restricts values to the currently visible fields, so a form
// submits nothing for a field the user could not see.
func VisibleValues(fields []providers.FormField, values map[string]string) map[string]string {
	vis := Visible(fields, values)
	out := make(map[string]string, len(values))
	for i, f := range fields {
		if !vis[i] {
			continue
		}
		if v, ok := values[f.Key]; ok {
			out[f.Key] = v
		}
	}
	return out
}

// FirstMissingRequired returns the index of the first visible required entry
// whose value is empty. ok is true when the form is submittable.
func FirstMissingRequired(fields []providers.FormField, values map[string]string) (int, bool) {
	vis := Visible(fields, values)
	for i, f := range fields {
		if KindOf(f) != KindEntry || !vis[i] {
			// Dropdowns, sliders and checkboxes always carry a value, and a
			// hidden field is never submitted, so neither can block the form.
			continue
		}
		if f.Required && strings.TrimSpace(values[f.Key]) == "" {
			return i, false
		}
	}
	return -1, true
}

// EnterSubmits reports whether Enter inside a form field submits it. A form
// with a dedicated submit button hands Enter to GTK instead, so a dropdown
// opens and commits its list and only the button submits.
func EnterSubmits(form providers.Form) bool {
	return form.SubmitLabel == ""
}
