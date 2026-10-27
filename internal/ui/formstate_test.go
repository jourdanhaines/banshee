package ui

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	mrand "math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestTrimValues(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"trims whitespace", map[string]string{"a": "  x  ", "b": "\ty\n"}, map[string]string{"a": "x", "b": "y"}},
		{"empty map", map[string]string{}, map[string]string{}},
		{"blank becomes empty", map[string]string{"a": "   "}, map[string]string{"a": ""}},
		{"inner newlines survive", map[string]string{"n": "\n line one\nline two \n"}, map[string]string{"n": "line one\nline two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TrimValues(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestFirstMissingRequired(t *testing.T) {
	fields := []providers.FormField{
		{Key: "a", Required: true},
		{Key: "b", Required: false},
		{Key: "c", Required: true},
	}
	tests := []struct {
		name   string
		fields []providers.FormField
		values map[string]string
		wantI  int
		wantOK bool
	}{
		{"all present", fields, map[string]string{"a": "1", "c": "2"}, -1, true},
		{"first required missing", fields, map[string]string{"c": "2"}, 0, false},
		{"later required missing", fields, map[string]string{"a": "1"}, 2, false},
		{"whitespace counts as missing", fields, map[string]string{"a": "  ", "c": "2"}, 0, false},
		{"optional missing is fine", fields, map[string]string{"a": "1", "c": "2"}, -1, true},
		{"no fields", nil, nil, -1, true},
		{
			"a required dropdown never blocks submission",
			[]providers.FormField{{Key: "backend", Required: true, Options: []string{"keyring", "plaintext"}}},
			map[string]string{"backend": ""},
			-1, true,
		},
		{
			"a dropdown is skipped in favour of a genuinely empty entry",
			[]providers.FormField{
				{Key: "backend", Required: true, Options: []string{"keyring", "plaintext"}},
				{Key: "name", Required: true},
			},
			map[string]string{},
			1, false,
		},
		{
			"an empty options slice is an ordinary entry",
			[]providers.FormField{{Key: "name", Required: true, Options: []string{}}},
			map[string]string{},
			0, false,
		},
		{
			"a hidden required empty entry never blocks submission",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "name", Required: true, ShowWhen: when("mode", "b")},
			},
			map[string]string{"mode": "a"},
			-1, true,
		},
		{
			"a required entry shown by its condition must be filled",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "name", Required: true, ShowWhen: when("mode", "b")},
			},
			map[string]string{"mode": "b"},
			1, false,
		},
		{
			"a required entry hidden through a chain is fine",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "sub", Options: []string{"x", "y"}, ShowWhen: when("mode", "b")},
				{Key: "name", Required: true, ShowWhen: when("sub", "x")},
			},
			map[string]string{"mode": "a", "sub": "x"},
			-1, true,
		},
		{
			"a required slider never blocks submission",
			[]providers.FormField{{Key: "n", Required: true, Slider: &providers.SliderSpec{Min: 1, Max: 5}}},
			map[string]string{},
			-1, true,
		},
		{
			"a required checkbox never blocks submission",
			[]providers.FormField{{Key: "c", Required: true, Checkbox: true}},
			map[string]string{"c": "false"},
			-1, true,
		},
		{
			"a required empty generated field never blocks submission",
			[]providers.FormField{{Key: "g", Required: true, Generate: &providers.GenerateSpec{Length: 8}}},
			map[string]string{"g": ""},
			-1, true,
		},
		{
			"an empty required multi-line field blocks submission",
			[]providers.FormField{{Key: "notes", Required: true, Multiline: true}},
			map[string]string{"notes": " \n\t"},
			0, false,
		},
		{
			"a filled required multi-line field is fine",
			[]providers.FormField{{Key: "notes", Required: true, Multiline: true}},
			map[string]string{"notes": "a\nb"},
			-1, true,
		},
		{
			"a hidden required multi-line field never blocks submission",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "notes", Required: true, Multiline: true, ShowWhen: when("mode", "b")},
			},
			map[string]string{"mode": "a"},
			-1, true,
		},
		{
			"a hidden empty entry is skipped for a later visible empty one",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "hidden", Required: true, ShowWhen: when("mode", "b")},
				{Key: "name", Required: true},
			},
			map[string]string{"mode": "a"},
			2, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := FirstMissingRequired(tt.fields, tt.values)
			if i != tt.wantI || ok != tt.wantOK {
				t.Errorf("FirstMissingRequired = (%d, %v), want (%d, %v)", i, ok, tt.wantI, tt.wantOK)
			}
		})
	}
}

func TestEnterSubmits(t *testing.T) {
	tests := []struct {
		name string
		form providers.Form
		want bool
	}{
		{"no submit label keeps Enter-submits", providers.Form{Title: "T"}, true},
		{"submit label hands Enter to GTK", providers.Form{SubmitLabel: "Start Recording"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EnterSubmits(tt.form); got != tt.want {
				t.Errorf("EnterSubmits = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormHint(t *testing.T) {
	tests := []struct {
		name string
		form providers.Form
		want string
	}{
		{"no fields", providers.Form{}, "Enter to save · Esc to go back"},
		{"entries only", providers.Form{Fields: []providers.FormField{{Key: "a"}}}, "Enter to save · Esc to go back"},
		{"a multi-line field", providers.Form{Fields: []providers.FormField{{Key: "a"}, {Key: "n", Multiline: true}}}, "Ctrl+Enter to save · Esc to go back"},
		{"a secret multi-line field is an entry", providers.Form{Fields: []providers.FormField{{Key: "n", Multiline: true, Secret: true}}}, "Enter to save · Esc to go back"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormHint(tt.form); got != tt.want {
				t.Errorf("FormHint = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKindOf(t *testing.T) {
	tests := []struct {
		name  string
		field providers.FormField
		want  FieldKind
	}{
		{"plain field is an entry", providers.FormField{Key: "a"}, KindEntry},
		{"empty options is an entry", providers.FormField{Options: []string{}}, KindEntry},
		{"options make a dropdown", providers.FormField{Options: []string{"x"}}, KindDropdown},
		{"valid slider", providers.FormField{Slider: &providers.SliderSpec{Min: 0, Max: 10}}, KindSlider},
		{"options beat slider", providers.FormField{Options: []string{"x"}, Slider: &providers.SliderSpec{Max: 10}}, KindDropdown},
		{"slider beats checkbox", providers.FormField{Slider: &providers.SliderSpec{Max: 10}, Checkbox: true}, KindSlider},
		{"empty-range slider falls through to checkbox", providers.FormField{Slider: &providers.SliderSpec{Min: 5, Max: 5}, Checkbox: true}, KindCheckbox},
		{"inverted slider alone falls through to entry", providers.FormField{Slider: &providers.SliderSpec{Min: 5, Max: 1}}, KindEntry},
		{"checkbox", providers.FormField{Checkbox: true}, KindCheckbox},
		{"checked without checkbox is an entry", providers.FormField{Checked: true}, KindEntry},
		{"generate alone is generated", providers.FormField{Generate: &providers.GenerateSpec{Length: 8}}, KindGenerated},
		{"zero generate spec is still generated", providers.FormField{Generate: &providers.GenerateSpec{}}, KindGenerated},
		{"checkbox beats generate", providers.FormField{Checkbox: true, Generate: &providers.GenerateSpec{}}, KindCheckbox},
		{"options beat generate", providers.FormField{Options: []string{"x"}, Generate: &providers.GenerateSpec{}}, KindDropdown},
		{"slider beats generate", providers.FormField{Slider: &providers.SliderSpec{Max: 10}, Generate: &providers.GenerateSpec{}}, KindSlider},
		{"multiline", providers.FormField{Multiline: true}, KindMultiline},
		{"generate beats multiline", providers.FormField{Generate: &providers.GenerateSpec{}, Multiline: true}, KindGenerated},
		{"checkbox beats multiline", providers.FormField{Checkbox: true, Multiline: true}, KindCheckbox},
		{"options beat multiline", providers.FormField{Options: []string{"x"}, Multiline: true}, KindDropdown},
		{"secret beats multiline", providers.FormField{Secret: true, Multiline: true}, KindEntry},
		{"value alone is an entry", providers.FormField{Value: "x"}, KindEntry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KindOf(tt.field); got != tt.want {
				t.Errorf("KindOf = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeSlider(t *testing.T) {
	tests := []struct {
		name   string
		in     providers.SliderSpec
		want   providers.SliderSpec
		wantOK bool
	}{
		{"zero step becomes 1", providers.SliderSpec{Min: 0, Max: 10, Default: 4}, providers.SliderSpec{Min: 0, Max: 10, Step: 1, Default: 4}, true},
		{"negative step becomes 1", providers.SliderSpec{Min: 0, Max: 10, Step: -3, Default: 4}, providers.SliderSpec{Min: 0, Max: 10, Step: 1, Default: 4}, true},
		{"step beyond range shrinks to it", providers.SliderSpec{Min: 2, Max: 8, Step: 50, Default: 2}, providers.SliderSpec{Min: 2, Max: 8, Step: 6, Default: 2}, true},
		{"default below clamps to Min", providers.SliderSpec{Min: 8, Max: 128, Step: 1, Default: 0}, providers.SliderSpec{Min: 8, Max: 128, Step: 1, Default: 8}, true},
		{"default above clamps to Max on grid", providers.SliderSpec{Min: 0, Max: 100, Step: 5, Default: 500}, providers.SliderSpec{Min: 0, Max: 100, Step: 5, Default: 100}, true},
		{"default above off-grid Max lands on last grid point", providers.SliderSpec{Min: 0, Max: 10, Step: 3, Default: 20}, providers.SliderSpec{Min: 0, Max: 10, Step: 3, Default: 9}, true},
		{"off-grid default snaps", providers.SliderSpec{Min: 0, Max: 100, Step: 10, Default: 47}, providers.SliderSpec{Min: 0, Max: 100, Step: 10, Default: 50}, true},
		{"empty range is not ok", providers.SliderSpec{Min: 5, Max: 5}, providers.SliderSpec{Min: 5, Max: 5}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NormalizeSlider(tt.in)
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Errorf("NormalizeSlider = (%+v, %v), want (%+v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestSnapSlider(t *testing.T) {
	tests := []struct {
		name string
		spec providers.SliderSpec
		raw  float64
		want int
	}{
		{"rounds down", providers.SliderSpec{Min: 0, Max: 10, Step: 1}, 3.4, 3},
		{"rounds up", providers.SliderSpec{Min: 0, Max: 10, Step: 1}, 3.6, 4},
		{"snaps to step below", providers.SliderSpec{Min: 0, Max: 100, Step: 5}, 7, 5},
		{"half step rounds up", providers.SliderSpec{Min: 0, Max: 100, Step: 5}, 7.5, 10},
		{"clamps below", providers.SliderSpec{Min: 0, Max: 100, Step: 5}, -40, 0},
		{"clamps above", providers.SliderSpec{Min: 0, Max: 100, Step: 5}, 140, 100},
		{"off-grid Max steps down", providers.SliderSpec{Min: 0, Max: 12, Step: 5}, 12, 10},
		{"negative range", providers.SliderSpec{Min: -10, Max: 10, Step: 5}, -7, -5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SnapSlider(tt.spec, tt.raw); got != tt.want {
				t.Errorf("SnapSlider(%v) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}

// cond builds one ShowWhen condition for test tables.
func cond(key string, values ...string) providers.Condition {
	return providers.Condition{Key: key, Values: values}
}

// when builds a single-condition ShowWhen list for test tables.
func when(key string, values ...string) []providers.Condition {
	return []providers.Condition{cond(key, values...)}
}

func TestVisible(t *testing.T) {
	gen := providers.FormField{Key: "gen", Options: []string{"text", "key", "uuid"}}
	sym := providers.FormField{Key: "sym", Checkbox: true}
	tests := []struct {
		name   string
		fields []providers.FormField
		values map[string]string
		want   []bool
	}{
		{"no show_when shows everything", []providers.FormField{{Key: "a"}, {Key: "b"}}, map[string]string{}, []bool{true, true}},
		{"dropdown match shows", []providers.FormField{gen, {Key: "len", ShowWhen: when("gen", "text")}}, map[string]string{"gen": "text"}, []bool{true, true}},
		{"dropdown mismatch hides", []providers.FormField{gen, {Key: "len", ShowWhen: when("gen", "text")}}, map[string]string{"gen": "key"}, []bool{true, false}},
		{"any of several values shows", []providers.FormField{gen, {Key: "enc", ShowWhen: when("gen", "text", "key")}}, map[string]string{"gen": "key"}, []bool{true, true}},
		{"checkbox reference", []providers.FormField{{Key: "adv", Checkbox: true}, {Key: "x", ShowWhen: when("adv", "true")}}, map[string]string{"adv": "true"}, []bool{true, true}},
		{"entry text reference", []providers.FormField{{Key: "name"}, {Key: "x", ShowWhen: when("name", "root")}}, map[string]string{"name": "alice"}, []bool{true, false}},
		{
			"hidden parent hides the chain",
			[]providers.FormField{gen, {Key: "b", Options: []string{"on"}, ShowWhen: when("gen", "text")}, {Key: "c", ShowWhen: when("b", "on")}},
			map[string]string{"gen": "key", "b": "on"},
			[]bool{true, false, false},
		},
		{
			"chain all matching",
			[]providers.FormField{gen, {Key: "b", Options: []string{"on"}, ShowWhen: when("gen", "text")}, {Key: "c", ShowWhen: when("b", "on")}},
			map[string]string{"gen": "text", "b": "on"},
			[]bool{true, true, true},
		},
		{"forward reference is ignored", []providers.FormField{{Key: "a", ShowWhen: when("b", "x")}, {Key: "b"}}, map[string]string{"b": "y"}, []bool{true, true}},
		{"self reference is ignored", []providers.FormField{{Key: "a", ShowWhen: when("a", "x")}}, map[string]string{"a": "y"}, []bool{true}},
		{"unknown key is ignored", []providers.FormField{{Key: "a", ShowWhen: when("nope", "x")}}, map[string]string{}, []bool{true}},
		{"empty values is ignored", []providers.FormField{gen, {Key: "a", ShowWhen: when("gen")}}, map[string]string{"gen": "text"}, []bool{true, true}},
		{"nil values map reads as empty", []providers.FormField{{Key: "a"}, {Key: "b", ShowWhen: when("a", "")}}, nil, []bool{true, true}},
		{
			"duplicate key resolves to the first",
			[]providers.FormField{gen, {Key: "a"}, {Key: "a", ShowWhen: when("gen", "key")}, {Key: "b", ShowWhen: when("a", "x")}},
			map[string]string{"gen": "text", "a": "x"},
			[]bool{true, true, false, true},
		},
		{
			"two conditions both true shows",
			[]providers.FormField{gen, sym, {Key: "x", ShowWhen: []providers.Condition{cond("gen", "text"), cond("sym", "true")}}},
			map[string]string{"gen": "text", "sym": "true"},
			[]bool{true, true, true},
		},
		{
			"one false condition hides",
			[]providers.FormField{gen, sym, {Key: "x", ShowWhen: []providers.Condition{cond("gen", "text"), cond("sym", "true")}}},
			map[string]string{"gen": "text", "sym": "false"},
			[]bool{true, true, false},
		},
		{
			"unresolvable plus true condition shows",
			[]providers.FormField{gen, {Key: "x", ShowWhen: []providers.Condition{cond("nope", "y"), cond("gen", "text")}}},
			map[string]string{"gen": "text"},
			[]bool{true, true},
		},
		{
			"unresolvable plus false condition hides",
			[]providers.FormField{gen, {Key: "x", ShowWhen: []providers.Condition{cond("nope", "y"), cond("gen", "key")}}},
			map[string]string{"gen": "text"},
			[]bool{true, false},
		},
		{"empty condition list shows", []providers.FormField{gen, {Key: "x", ShowWhen: []providers.Condition{}}}, map[string]string{"gen": "text"}, []bool{true, true}},
		{
			"hidden referenced field in a second condition hides",
			[]providers.FormField{gen, {Key: "sym", Checkbox: true, ShowWhen: when("gen", "text")}, {Key: "x", ShowWhen: []providers.Condition{cond("gen", "text", "key"), cond("sym", "true")}}},
			map[string]string{"gen": "key", "sym": "true"},
			[]bool{true, false, false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Visible(tt.fields, tt.values); !slices.Equal(got, tt.want) {
				t.Errorf("Visible = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVisibleValues(t *testing.T) {
	fields := []providers.FormField{
		{Key: "gen", Options: []string{"text", "key"}},
		{Key: "len", ShowWhen: when("gen", "text")},
		{Key: "bits", ShowWhen: when("gen", "key")},
		{Key: "note"},
	}
	tests := []struct {
		name   string
		fields []providers.FormField
		values map[string]string
		want   map[string]string
	}{
		{
			"hidden values are dropped",
			fields,
			map[string]string{"gen": "text", "len": "24", "bits": "256", "note": "hi"},
			map[string]string{"gen": "text", "len": "24", "note": "hi"},
		},
		{
			"visible empty strings are kept",
			fields,
			map[string]string{"gen": "key", "len": "", "bits": "", "note": ""},
			map[string]string{"gen": "key", "bits": "", "note": ""},
		},
		{
			"no show_when keeps the full map",
			[]providers.FormField{{Key: "a"}, {Key: "b"}},
			map[string]string{"a": "1", "b": "2"},
			map[string]string{"a": "1", "b": "2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VisibleValues(tt.fields, tt.values); !maps.Equal(got, tt.want) {
				t.Errorf("VisibleValues = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGenerateLength(t *testing.T) {
	tests := []struct {
		name   string
		spec   providers.GenerateSpec
		values map[string]string
		want   int
	}{
		{"length_from parses", providers.GenerateSpec{LengthFrom: "len", Length: 10}, map[string]string{"len": "24"}, 24},
		{"length_from trims", providers.GenerateSpec{LengthFrom: "len"}, map[string]string{"len": " 32 "}, 32},
		{"non-int falls back to length", providers.GenerateSpec{LengthFrom: "len", Length: 10}, map[string]string{"len": "abc"}, 10},
		{"zero falls back to length", providers.GenerateSpec{LengthFrom: "len", Length: 10}, map[string]string{"len": "0"}, 10},
		{"negative falls back to length", providers.GenerateSpec{LengthFrom: "len", Length: 10}, map[string]string{"len": "-5"}, 10},
		{"missing key falls back to length", providers.GenerateSpec{LengthFrom: "len", Length: 10}, nil, 10},
		{"nothing set defaults to 16", providers.GenerateSpec{}, nil, 16},
		{"non-int with no length defaults to 16", providers.GenerateSpec{LengthFrom: "len"}, map[string]string{"len": "x"}, 16},
		{"negative length defaults to 16", providers.GenerateSpec{Length: -3}, nil, 16},
		{"minimum of 1 kept", providers.GenerateSpec{LengthFrom: "len"}, map[string]string{"len": "1"}, 1},
		{"length_from clamps to 256", providers.GenerateSpec{LengthFrom: "len"}, map[string]string{"len": "1000"}, 256},
		{"length clamps to 256", providers.GenerateSpec{Length: 257}, nil, 256},
		{"256 kept", providers.GenerateSpec{Length: 256}, nil, 256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GenerateLength(tt.spec, tt.values); got != tt.want {
				t.Errorf("GenerateLength = %d, want %d", got, tt.want)
			}
		})
	}
}

// mustGenerate is GenerateValue failing the test on a reader error.
func mustGenerate(t *testing.T, spec providers.GenerateSpec, values map[string]string) string {
	t.Helper()
	v, err := GenerateValue(spec, values)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGenerateValuePattern(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		charset string
		want    *regexp.Regexp
	}{
		{"x picks from charset", "x", "ab", regexp.MustCompile(`^[ab]$`)},
		{"bracket set picks from its runes", "[89ab]", "z", regexp.MustCompile(`^[89ab]$`)},
		{"escaped x is literal", `\x`, "ab", regexp.MustCompile(`^x$`)},
		{"escaped bracket is literal", `\[x]`, "a", regexp.MustCompile(`^\[a\]$`)},
		{"trailing backslash is literal", `x\`, "a", regexp.MustCompile(`^a\\$`)},
		{"unterminated bracket is literal", "[ab", "z", regexp.MustCompile(`^\[ab$`)},
		{"empty set is literal", "[]x", "z", regexp.MustCompile(`^\[\]z$`)},
		{"mixed literals", "id-xx_[7]!", "q", regexp.MustCompile(`^id-qq_7!$`)},
		{"multibyte set and literal", "é[αβ]x", "γ", regexp.MustCompile(`^é[αβ]γ$`)},
		{
			"uuid v4 pattern",
			"xxxxxxxx-xxxx-4xxx-[89ab]xxx-xxxxxxxxxxxx", "0123456789abcdef",
			regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 20 {
				got := mustGenerate(t, providers.GenerateSpec{Pattern: tt.pattern, Charset: tt.charset, Length: 99}, nil)
				if !tt.want.MatchString(got) {
					t.Fatalf("GenerateValue(%q) = %q, want match %s", tt.pattern, got, tt.want)
				}
			}
		})
	}
}

func TestGenerateValueBytes(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int
		encoding string
		wantLen  int
		suffix   string
		decode   func(string) ([]byte, error)
	}{
		{"hex 16", 16, "hex", 32, "", hex.DecodeString},
		{"hex 32", 32, "hex", 64, "", hex.DecodeString},
		{"unknown encoding is hex", 16, "rot13", 32, "", hex.DecodeString},
		{"empty encoding is hex", 8, "", 16, "", hex.DecodeString},
		{"base64 16", 16, "base64", 24, "==", base64.StdEncoding.DecodeString},
		{"base64 32", 32, "base64", 44, "=", base64.StdEncoding.DecodeString},
		{"base64 64", 64, "base64", 88, "==", base64.StdEncoding.DecodeString},
		{"base64url 16", 16, "base64url", 22, "", base64.RawURLEncoding.DecodeString},
		{"base64url 32", 32, "base64url", 43, "", base64.RawURLEncoding.DecodeString},
		{"base64url 64", 64, "base64url", 86, "", base64.RawURLEncoding.DecodeString},
		{"bytes cap at 256", 1000, "hex", 512, "", hex.DecodeString},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Bytes wins over a pattern and charset set alongside it.
			spec := providers.GenerateSpec{Bytes: tt.bytes, Encoding: tt.encoding, Pattern: "x", Charset: "a"}
			got := mustGenerate(t, spec, nil)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d (%q)", len(got), tt.wantLen, got)
			}
			if tt.suffix != "" && !strings.HasSuffix(got, tt.suffix) {
				t.Errorf("%q lacks padding %q", got, tt.suffix)
			}
			if tt.encoding == "base64url" && strings.ContainsAny(got, "+/=") {
				t.Errorf("%q is not raw base64url", got)
			}
			raw, err := tt.decode(got)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) != min(tt.bytes, 256) {
				t.Errorf("decoded %d bytes, want %d", len(raw), min(tt.bytes, 256))
			}
		})
	}
}

// passgenSymbolCharset is a full password alphabet: alnum plus every symbol
// a text password may draw from.
const passgenSymbolCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*()-_=+[]{};:,.<>?/"

func TestGenerateValueCharset(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"picks stay in the charset", func(t *testing.T) {
			got := mustGenerate(t, providers.GenerateSpec{Charset: "abc€", Length: 200}, nil)
			if n := utf8.RuneCountInString(got); n != 200 {
				t.Fatalf("rune count = %d, want 200", n)
			}
			for _, r := range got {
				if !strings.ContainsRune("abc€", r) {
					t.Fatalf("rune %q outside charset", r)
				}
			}
		}},
		{"every charset rune appears", func(t *testing.T) {
			// Proves the charset is iterated as runes with nothing dropped:
			// 88 runes over up to 20 rolls of 256 picks.
			missing := map[rune]bool{}
			for _, r := range passgenSymbolCharset {
				missing[r] = true
			}
			for roll := 0; roll < 20 && len(missing) > 0; roll++ {
				for _, r := range mustGenerate(t, providers.GenerateSpec{Charset: passgenSymbolCharset, Length: 256}, nil) {
					delete(missing, r)
				}
			}
			if len(missing) > 0 {
				t.Errorf("never generated %q", slices.Collect(maps.Keys(missing)))
			}
		}},
		{"length_from sets the count", func(t *testing.T) {
			got := mustGenerate(t, providers.GenerateSpec{Charset: "ab", LengthFrom: "len"}, map[string]string{"len": "40"})
			if len(got) != 40 {
				t.Errorf("len = %d, want 40", len(got))
			}
		}},
		{"two rolls differ", func(t *testing.T) {
			spec := providers.GenerateSpec{Charset: passgenSymbolCharset, Length: 32}
			if a, b := mustGenerate(t, spec, nil), mustGenerate(t, spec, nil); a == b {
				t.Errorf("two rolls both %q", a)
			}
		}},
		{"empty charset generates nothing", func(t *testing.T) {
			for _, spec := range []providers.GenerateSpec{{Length: 8}, {Pattern: "xx-[ab]"}} {
				if got := mustGenerate(t, spec, nil); got != "" {
					t.Errorf("GenerateValue(%+v) = %q, want empty", spec, got)
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// errExhausted is failReader's error once it runs dry.
var errExhausted = errors.New("entropy exhausted")

// failReader is an entropy source that runs dry after n bytes.
type failReader struct{ n int }

func (f *failReader) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errExhausted
	}
	k := min(len(p), f.n)
	for i := range k {
		p[i] = 'A'
	}
	f.n -= k
	return k, nil
}

func TestGenerateValueReaderFailure(t *testing.T) {
	tests := []struct {
		name string
		spec providers.GenerateSpec
	}{
		{"bytes", providers.GenerateSpec{Bytes: 32}},
		{"charset", providers.GenerateSpec{Charset: "abcdefgh", Length: 64}},
		{"pattern", providers.GenerateSpec{Charset: "abcdefgh", Pattern: "xxxxxxxx-[xy]xxxxxxx"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := generateValue(&failReader{n: 8}, tt.spec, nil)
			if err == nil {
				t.Fatalf("err = nil, value %q", got)
			}
			if got != "" {
				t.Errorf("value = %q, want empty on failure", got)
			}
			// The message is fixed text around the reader's error, so it
			// cannot carry any of the runes picked before the failure.
			msg := strings.TrimSuffix(err.Error(), ": "+errExhausted.Error())
			if !errors.Is(err, errExhausted) || (msg != "generate: read random bytes" && msg != "generate: pick random rune") {
				t.Errorf("error %q is not the fixed failure text", err)
			}
		})
	}
}

func TestObscureMask(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"two", 2, 1},
		{"three", 3, 1},
		{"four", 4, 1},
		{"eight", 8, 2},
		{"twenty-four", 24, 6},
		{"hundred", 100, 25},
		{"max length", 256, 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, src := range []*mrand.Rand{nil, mrand.New(mrand.NewPCG(1, 2))} {
				mask := ObscureMask(tt.n, src)
				if len(mask) != tt.n {
					t.Fatalf("len(mask) = %d, want %d", len(mask), tt.n)
				}
				got := 0
				for _, m := range mask {
					if m {
						got++
					}
				}
				if got != tt.want {
					t.Errorf("obscured %d of %d, want %d", got, tt.n, tt.want)
				}
			}
		})
	}
}

func TestObscureMaskVaries(t *testing.T) {
	tests := []struct {
		name string
		n    int
	}{
		{"twenty-four runes", 24},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mrand.New(mrand.NewPCG(1, 2))
			a, b := ObscureMask(tt.n, r), ObscureMask(tt.n, r)
			if slices.Equal(a, b) {
				t.Errorf("two masks from one source are identical: %v", a)
			}
		})
	}
}

func TestObscureSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		mask []bool
		want string
	}{
		{"empty", "", nil, ""},
		{"empty with mask", "", []bool{true}, ""},
		{"masked positions", "abcd", []bool{true, false, false, true}, "•bc•"},
		{"multibyte runes", "αβγé", []bool{false, true, false, true}, "α•γ•"},
		{"short mask leaves tail visible", "abcdef", []bool{true, true}, "••cdef"},
		{"long mask truncated", "ab", []bool{false, true, true, true}, "a•"},
		{"nil mask shows all", "abc", nil, "abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ObscureSecret(tt.in, tt.mask); got != tt.want {
				t.Errorf("ObscureSecret = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyRune(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want RuneClass
	}{
		{"ascii upper", 'Q', RuneAlpha},
		{"ascii lower", 'l', RuneAlpha},
		{"non-ascii letter", 'é', RuneAlpha},
		{"digit", '0', RuneDigit},
		{"bullet", '•', RuneBullet},
		{"pipe", '|', RuneSymbol},
		{"dash", '-', RuneSymbol},
		{"angle", '<', RuneSymbol},
		{"space", ' ', RuneSymbol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyRune(tt.r); got != tt.want {
				t.Errorf("ClassifyRune(%q) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

func TestSecretMarkup(t *testing.T) {
	span := func(colour, text string) string {
		return `<span foreground="` + colour + `">` + text + `</span>`
	}
	const accent = "#7aa2f7"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"runs merge by class", "ab12", span(SecretAlphaColor, "ab") + span(accent, "12")},
		{
			"each class change opens a span",
			"aB9-x•",
			span(SecretAlphaColor, "aB") + span(accent, "9") + span(SecretSymbolColor, "-") +
				span(SecretAlphaColor, "x") + span(SecretBulletColor, "•"),
		},
		{"markup characters are escaped", "<&>", span(SecretSymbolColor, "&lt;&amp;&gt;")},
		{
			"obscured value",
			ObscureSecret("ab12cd", []bool{false, true, false, true, true, false}),
			span(SecretAlphaColor, "a") + span(SecretBulletColor, "•") + span(accent, "1") +
				span(SecretBulletColor, "••") + span(SecretAlphaColor, "d"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SecretMarkup(tt.in, accent); got != tt.want {
				t.Errorf("SecretMarkup = %q, want %q", got, tt.want)
			}
		})
	}
}
