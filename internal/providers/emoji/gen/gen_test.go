package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func openFixture(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func fixtureRows(t *testing.T) []row {
	t.Helper()
	rows, _, err := parseEmojiTest(openFixture(t, "emoji-test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func fixtureAnnotations(t *testing.T, name string) map[string][]string {
	t.Helper()
	m, err := parseAnnotations(openFixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mkRow(name string, cps ...rune) row {
	return row{runes: cps, value: string(cps), name: name}
}

func values(rs []row) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.value
	}
	return out
}

func TestParseEmojiTest(t *testing.T) {
	rows, version, err := parseEmojiTest(openFixture(t, "emoji-test.txt"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.value] = true
	}
	tests := []struct {
		name  string
		check func() bool
	}{
		{"version", func() bool { return version == "18.0" }},
		{"row count", func() bool { return len(rows) == 25 }},
		{"order", func() bool {
			want := []string{"😀", "😃", "☝️", "☝🏻", "☝🏼", "☝🏽", "☝🏾", "☝🏿", "👋", "👋🏻", "👋🏼", "👋🏽", "👋🏾", "👋🏿",
				"👨", "👨‍🦰", "👨🏻‍🦰", "💏", "💏🏻", "🧑🏻‍❤️‍💋‍🧑🏼", "🤝", "🫱🏻‍🫲🏼", "#️⃣", "🇺🇸", "🏴󠁧󠁢󠁥󠁮󠁧󠁿"}
			return reflect.DeepEqual(values(rows), want)
		}},
		{"unqualified skipped", func() bool { return !got["☝"] && !got["#⃣"] }},
		{"minimally-qualified skipped", func() bool { return !got["🧑🏻‍❤‍💋‍🧑🏼"] }},
		{"components skipped", func() bool { return !got["🏻"] && !got["🦰"] }},
		{"name", func() bool { return rows[16].name == "man: light skin tone, red hair" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.check() {
				t.Errorf("check failed; rows = %q (version %q)", values(rows), version)
			}
		})
	}
}

func TestParseEmojiTestErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bad hex", "1F60G ; fully-qualified # 😀 E1.0 grinning face\n", "line 1: bad code point"},
		{"invalid rune", "D800 ; fully-qualified # x E1.0 surrogate\n", "line 1: invalid rune"},
		{"glyph mismatch", "# Version: 18.0\n1F600 ; fully-qualified # 😃 E1.0 grinning face\n", "line 2: glyph"},
		{"missing semicolon", "1F600 fully-qualified # 😀 E1.0 grinning face\n", "line 1: missing ';'"},
		{"name with tab", "1F600 ; fully-qualified # 😀 E1.0 grinning\tface\n", "line 1: name contains TAB"},
		{"short comment", "1F600 ; fully-qualified # 😀 E1.0\n", "line 1: malformed comment"},
		{"no E version", "1F600 ; fully-qualified # 😀 1.0 grinning face\n", "line 1: malformed comment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseEmojiTest(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestGroup(t *testing.T) {
	es, err := group(fixtureRows(t))
	if err != nil {
		t.Fatal(err)
	}
	byValue := map[string]entry{}
	var bases []string
	for _, e := range es {
		byValue[e.value] = e
		bases = append(bases, e.value)
	}
	tests := []struct {
		name string
		base string
		want []string
	}{
		{"VS16 base takes toned rows", "☝️", []string{"☝🏻", "☝🏼", "☝🏽", "☝🏾", "☝🏿"}},
		{"plain tones", "👋", []string{"👋🏻", "👋🏼", "👋🏽", "👋🏾", "👋🏿"}},
		{"ZWJ variant under ZWJ base", "👨‍🦰", []string{"👨🏻‍🦰"}},
		{"ZWJ variant not under prefix", "👨", nil},
		{"mixed kiss after uniform", "💏", []string{"💏🏻", "🧑🏻‍❤️‍💋‍🧑🏼"}},
		{"mixed handshake", "🤝", []string{"🫱🏻‍🫲🏼"}},
		{"opaque keycap", "#️⃣", nil},
		{"opaque tag flag", "🏴󠁧󠁢󠁥󠁮󠁧󠁿", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := byValue[tt.base]
			if !ok {
				t.Fatalf("no base %q", tt.base)
			}
			if got := values(e.variants); len(got) != len(tt.want) || (len(got) > 0 && !reflect.DeepEqual(got, tt.want)) {
				t.Errorf("variants = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("base order", func(t *testing.T) {
		want := []string{"😀", "😃", "☝️", "👋", "👨", "👨‍🦰", "💏", "🤝", "#️⃣", "🇺🇸", "🏴󠁧󠁢󠁥󠁮󠁧󠁿"}
		if !reflect.DeepEqual(bases, want) {
			t.Errorf("bases = %q, want %q", bases, want)
		}
	})
}

func TestGroupRules(t *testing.T) {
	redHair := mkRow("man: red hair", 0x1F468, 0x200D, 0x1F9B0)
	tests := []struct {
		name     string
		rows     []row
		wantErr  string
		wantVars []string
	}{
		{
			name:     "name rule when sequence differs",
			rows:     []row{redHair, mkRow("man: light skin tone, red hair", 0x1F468, 0x1F3FB, 0x200D, 0x1F9B1)},
			wantVars: []string{"👨🏻‍🦱"},
		},
		{
			name:     "duplicate variant skipped",
			rows:     []row{mkRow("waving hand", 0x1F44B), mkRow("waving hand: light skin tone", 0x1F44B, 0x1F3FB), mkRow("waving hand: light skin tone", 0x1F44B, 0x1F3FB)},
			wantVars: []string{"👋🏻"},
		},
		{
			name:    "orphan variant",
			rows:    []row{mkRow("waving hand: light skin tone", 0x1F44B, 0x1F3FB)},
			wantErr: "no base for",
		},
		{
			name:    "duplicate base name",
			rows:    []row{mkRow("grinning face", 0x1F600), mkRow("grinning face", 0x1F603)},
			wantErr: "duplicate base name",
		},
		{
			name:    "duplicate base sequence",
			rows:    []row{mkRow("index pointing up", 0x261D, 0xFE0F), mkRow("index up", 0x261D)},
			wantErr: "duplicate base sequence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			es, err := group(tt.rows)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := values(es[0].variants); !reflect.DeepEqual(got, tt.wantVars) {
				t.Errorf("variants = %q, want %q", got, tt.wantVars)
			}
		})
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"waving hand", "waving hand"},
		{"waving hand: light skin tone", "waving hand"},
		{"man: light skin tone, red hair", "man: red hair"},
		{"kiss: light skin tone", "kiss"},
		{"handshake: light skin tone, medium skin tone", "handshake"},
		{"kiss: person, person, light skin tone, medium-light skin tone", "kiss: person, person"},
		{"flag: United States", "flag: United States"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := baseName(tt.in); got != tt.want {
				t.Errorf("baseName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseAnnotations(t *testing.T) {
	base := fixtureAnnotations(t, "annotations.xml")
	derived := fixtureAnnotations(t, "annotationsDerived.xml")
	tests := []struct {
		name string
		m    map[string][]string
		key  string
		want []string
	}{
		{"tts skipped, split and trimmed", base, "😀", []string{"face", "grin"}},
		{"FE0F-free key", base, norm([]rune("☝️")), []string{"finger", "hand", "index", "point", "up"}},
		{"raw duplicates kept for attach", base, "👋", []string{"hand", "wave", "Waving Hand", "waving", "hand"}},
		{"derived keycap", derived, norm([]rune("#️⃣")), []string{"keycap"}},
		{"derived ZWJ", derived, "👨‍🦰", []string{"adult", "man", "red hair"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m[tt.key]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("m[%q] = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
	t.Run("malformed xml", func(t *testing.T) {
		if _, err := parseAnnotations(strings.NewReader("<ldml><annotations>")); err == nil {
			t.Error("want error")
		}
	})
}

func TestAttachKeywords(t *testing.T) {
	base := fixtureAnnotations(t, "annotations.xml")
	derived := fixtureAnnotations(t, "annotationsDerived.xml")
	es, err := group(fixtureRows(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := attachKeywords(es, base, derived); err != nil {
		t.Fatal(err)
	}
	byValue := map[string][]string{}
	for _, e := range es {
		byValue[e.value] = e.keywords
	}
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{"base beats derived", "😀", []string{"face", "grin"}},
		{"derived fallback keycap", "#️⃣", []string{"keycap"}},
		{"derived fallback flag", "🇺🇸", []string{"flag"}},
		{"label dropped and deduped", "👋", []string{"hand", "wave", "waving"}},
		{"no source", "😃", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := byValue[tt.value]; !reflect.DeepEqual(got, tt.want) {
				t.Errorf("keywords = %q, want %q", got, tt.want)
			}
		})
	}
	t.Run("bad keyword", func(t *testing.T) {
		es := []entry{{row: mkRow("grinning face", 0x1F600)}}
		err := attachKeywords(es, map[string][]string{"😀": {"bad\tkw"}})
		if err == nil {
			t.Error("want error")
		}
	})
}

func TestWriteErrors(t *testing.T) {
	tests := []struct {
		name string
		es   []entry
	}{
		{"tab in label", []entry{{row: mkRow("a\tb", 0x1F600)}}},
		{"pipe in keyword", []entry{{row: mkRow("grinning face", 0x1F600), keywords: []string{"a|b"}}}},
		{"tab in variant", []entry{{row: mkRow("waving hand", 0x1F44B), variants: []row{mkRow("x\ty", 0x1F44B, 0x1F3FB)}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := write(&bytes.Buffer{}, tt.es, nil); err == nil {
				t.Error("want error")
			}
		})
	}
}

func TestWriteGolden(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "emoji.tsv")
	err := run(t.Context(), out,
		filepath.Join("testdata", "emoji-test.txt"),
		filepath.Join("testdata", "annotations.xml"),
		filepath.Join("testdata", "annotationsDerived.xml"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "golden.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from golden.tsv:\n%s", got)
	}
}

func TestRunVersionMismatch(t *testing.T) {
	src := filepath.Join(t.TempDir(), "emoji-test.txt")
	if err := os.WriteFile(src, []byte("# Version: 17.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), filepath.Join(t.TempDir(), "out.tsv"), src, "unused", "unused")
	if err == nil || !strings.Contains(err.Error(), `version "17.0"`) {
		t.Errorf("err = %v, want version mismatch", err)
	}
}

func TestNorm(t *testing.T) {
	tests := []struct {
		name string
		in   []rune
		want string
	}{
		{"no FE0F", []rune{0x1F600}, "😀"},
		{"trailing FE0F", []rune{0x261D, 0xFE0F}, "☝"},
		{"every FE0F", []rune{0x23, 0xFE0F, 0x20E3, 0xFE0F}, "#⃣"},
		{"empty", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := norm(tt.in); got != tt.want {
				t.Errorf("norm = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripModifiers(t *testing.T) {
	tests := []struct {
		name string
		in   []rune
		want []rune
	}{
		{"none", []rune{0x1F44B}, []rune{0x1F44B}},
		{"one", []rune{0x1F44B, 0x1F3FB}, []rune{0x1F44B}},
		{"both ends of range", []rune{0x1FAF1, 0x1F3FB, 0x200D, 0x1FAF2, 0x1F3FF}, []rune{0x1FAF1, 0x200D, 0x1FAF2}},
		{"outside range kept", []rune{0x1F3FA, 0x1F400}, []rune{0x1F3FA, 0x1F400}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripModifiers(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("stripModifiers = %U, want %U", got, tt.want)
			}
		})
	}
}
