package ui

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode"

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
	KindGenerated
)

// KindOf resolves a field's widget kind by precedence Options > Slider >
// Checkbox > Generate > entry. A slider whose Max <= Min is ignored, so the
// field falls through to the next kind rather than rendering an unusable range.
func KindOf(f providers.FormField) FieldKind {
	switch {
	case len(f.Options) > 0:
		return KindDropdown
	case f.Slider != nil && f.Slider.Max > f.Slider.Min:
		return KindSlider
	case f.Checkbox:
		return KindCheckbox
	case f.Generate != nil:
		return KindGenerated
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
			// Dropdowns, sliders, checkboxes and generated fields always
			// carry a value, and a hidden field is never submitted.
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

// Generation bounds: a pick-based value defaults to defaultGenerateLength and
// never exceeds maxGenerateLength runes; Bytes is capped the same way so a
// plugin cannot make the launcher allocate without limit.
const (
	defaultGenerateLength = 16
	maxGenerateLength     = 256
)

// GenerateLength is the pick count for a charset-only spec: the positive
// integer value of the LengthFrom field, else Length, else 16, clamped to
// 1..256. Pattern and Bytes specs ignore it.
func GenerateLength(spec providers.GenerateSpec, values map[string]string) int {
	n := 0
	if spec.LengthFrom != "" {
		if v, err := strconv.Atoi(strings.TrimSpace(values[spec.LengthFrom])); err == nil && v > 0 {
			n = v
		}
	}
	if n == 0 && spec.Length > 0 {
		n = spec.Length
	}
	if n == 0 {
		n = defaultGenerateLength
	}
	return min(max(n, 1), maxGenerateLength)
}

// GenerateValue generates a FormField.Generate value from crypto/rand per the
// GenerateSpec resolution order. The result is secret material: callers must
// never log it, and the error (reader failure only) never carries it.
func GenerateValue(spec providers.GenerateSpec, values map[string]string) (string, error) {
	return generateValue(rand.Reader, spec, values)
}

// generateValue is GenerateValue over an explicit entropy source, so a test
// can prove a failing reader yields an error and no partial value.
func generateValue(r io.Reader, spec providers.GenerateSpec, values map[string]string) (string, error) {
	if spec.Bytes > 0 {
		buf := make([]byte, min(spec.Bytes, maxGenerateLength))
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", fmt.Errorf("generate: read random bytes: %w", err)
		}
		switch spec.Encoding {
		case "base64":
			return base64.StdEncoding.EncodeToString(buf), nil
		case "base64url":
			return base64.RawURLEncoding.EncodeToString(buf), nil
		}
		return hex.EncodeToString(buf), nil
	}
	charset := []rune(spec.Charset)
	if len(charset) == 0 {
		return "", nil
	}
	var b strings.Builder
	pick := func(set []rune) error {
		i, err := rand.Int(r, big.NewInt(int64(len(set))))
		if err != nil {
			return fmt.Errorf("generate: pick random rune: %w", err)
		}
		b.WriteRune(set[i.Int64()])
		return nil
	}
	if spec.Pattern == "" {
		for range GenerateLength(spec, values) {
			if err := pick(charset); err != nil {
				return "", err
			}
		}
		return b.String(), nil
	}
	pat := []rune(spec.Pattern)
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '\\':
			// A trailing backslash has nothing to escape and stays literal.
			if i+1 < len(pat) {
				i++
			}
			b.WriteRune(pat[i])
		case 'x':
			if err := pick(charset); err != nil {
				return "", err
			}
		case '[':
			end := slices.Index(pat[i+1:], ']')
			if end <= 0 {
				// Unterminated `[` and empty `[]` are literal text.
				b.WriteRune(c)
				continue
			}
			if err := pick(pat[i+1 : i+1+end]); err != nil {
				return "", err
			}
			i += end + 1
		default:
			b.WriteRune(c)
		}
	}
	return b.String(), nil
}

// ElideSecret shortens a long secret for its hidden display: more than 24
// runes become the first 8, six bullets and the last 8, so the box stays one
// line while the user can still recognise the value.
func ElideSecret(s string) string {
	r := []rune(s)
	if len(r) <= 24 {
		return s
	}
	return string(r[:8]) + "••••••" + string(r[len(r)-8:])
}

// RuneClass groups a secret's runes for colouring, so look-alike characters
// (O and 0, l and |) read differently at a glance.
type RuneClass int

// The rune classes ClassifyRune assigns.
const (
	RuneAlpha RuneClass = iota
	RuneDigit
	RuneSymbol
	RuneBullet
)

// ClassifyRune returns r's colour class: letters, digits, the elision bullet,
// and everything else as a symbol.
func ClassifyRune(r rune) RuneClass {
	switch {
	case unicode.IsLetter(r):
		return RuneAlpha
	case unicode.IsDigit(r):
		return RuneDigit
	case r == '•':
		return RuneBullet
	}
	return RuneSymbol
}

// Secret display colours; digits take the theme accent instead.
const (
	// SecretAlphaColor colours letters.
	SecretAlphaColor = "#ffffff"
	// SecretBulletColor dims the elision bullets so they read as a gap.
	SecretBulletColor = "#6b7080"
	// SecretSymbolColor colours punctuation and every other rune.
	SecretSymbolColor = "#f7768e"
)

// SecretMarkup renders s as Pango markup with one coloured span per run of
// same-class runes (digits in accentHex), escaped by hand so this file stays
// display-free.
func SecretMarkup(s, accentHex string) string {
	colour := func(c RuneClass) string {
		switch c {
		case RuneAlpha:
			return SecretAlphaColor
		case RuneDigit:
			return accentHex
		case RuneBullet:
			return SecretBulletColor
		}
		return SecretSymbolColor
	}
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		class := ClassifyRune(runes[i])
		j := i + 1
		for j < len(runes) && ClassifyRune(runes[j]) == class {
			j++
		}
		b.WriteString(`<span foreground="` + markupEscape(colour(class)) + `">`)
		b.WriteString(markupEscape(string(runes[i:j])))
		b.WriteString(`</span>`)
		i = j
	}
	return b.String()
}

// markupEscape escapes the characters Pango markup treats as syntax.
func markupEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
