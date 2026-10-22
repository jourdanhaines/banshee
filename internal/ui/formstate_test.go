package ui

import (
	"maps"
	"slices"
	"testing"

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
				{Key: "name", Required: true, ShowWhen: &providers.ShowWhen{Key: "mode", Values: []string{"b"}}},
			},
			map[string]string{"mode": "a"},
			-1, true,
		},
		{
			"a required entry shown by its condition must be filled",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "name", Required: true, ShowWhen: &providers.ShowWhen{Key: "mode", Values: []string{"b"}}},
			},
			map[string]string{"mode": "b"},
			1, false,
		},
		{
			"a required entry hidden through a chain is fine",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "sub", Options: []string{"x", "y"}, ShowWhen: &providers.ShowWhen{Key: "mode", Values: []string{"b"}}},
				{Key: "name", Required: true, ShowWhen: &providers.ShowWhen{Key: "sub", Values: []string{"x"}}},
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
			"a hidden empty entry is skipped for a later visible empty one",
			[]providers.FormField{
				{Key: "mode", Options: []string{"a", "b"}},
				{Key: "hidden", Required: true, ShowWhen: &providers.ShowWhen{Key: "mode", Values: []string{"b"}}},
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

// when builds a ShowWhen condition for test tables.
func when(key string, values ...string) *providers.ShowWhen {
	return &providers.ShowWhen{Key: key, Values: values}
}

func TestVisible(t *testing.T) {
	gen := providers.FormField{Key: "gen", Options: []string{"text", "key", "uuid"}}
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
