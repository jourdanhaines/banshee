package emoji

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		name  string
		q     string
		on    bool
		query string
	}{
		{"min prefix", "emo", true, ""},
		{"upper", "EMO", true, ""},
		{"partial", "emoj", true, ""},
		{"full", "emoji", true, ""},
		{"capitalized", "Emoji", true, ""},
		{"with query", "emoji smile", true, "smile"},
		{"extra spaces", "emo  thumbs up ", true, "thumbs up"},
		{"leading spaces", "  emoj heart", true, "heart"},
		{"too short", "em", false, ""},
		{"longer word", "emotion", false, ""},
		{"past keyword", "emojis", false, ""},
		{"diverges", "emox", false, ""},
		{"empty", "", false, ""},
		{"unrelated", "open repo", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on, query := parseTrigger(tt.q)
			if on != tt.on || query != tt.query {
				t.Fatalf("parseTrigger(%q) = (%v, %q), want (%v, %q)", tt.q, on, query, tt.on, tt.query)
			}
		})
	}
}

func TestQueryUntriggered(t *testing.T) {
	p := New(fuzzy.Score)
	tests := []struct {
		name      string
		q         string
		wantRow   bool
		triggered bool
		query     string
	}{
		{"fuzzy title", "picker", true, false, ""},
		{"trigger with query", "emoji pick", true, true, "pick"},
		{"no match", "zzz", false, false, ""},
		{"empty", "", false, false, ""},
		{"blank", "   ", false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := p.Query(context.Background(), tt.q)
			if err != nil {
				t.Fatalf("Query(%q) err = %v", tt.q, err)
			}
			if !tt.wantRow {
				if got != nil {
					t.Fatalf("Query(%q) = %v, want nil", tt.q, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("Query(%q) returned %d rows, want 1", tt.q, len(got))
			}
			r := got[0]
			if tt.triggered {
				if r.Score != TriggerScore {
					t.Errorf("Score = %d, want %d", r.Score, TriggerScore)
				}
			} else if r.Score <= 0 || r.Score >= TriggerScore {
				t.Errorf("Score = %d, want in (0, %d)", r.Score, TriggerScore)
			}
			if r.Picker == nil || r.Picker.Query != tt.query {
				t.Errorf("Picker = %+v, want Query %q", r.Picker, tt.query)
			}
		})
	}
}

func TestQueryTriggeredShape(t *testing.T) {
	p := New(fuzzy.Score)
	got, err := p.Query(context.Background(), "emoji smile")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	r := got[0]
	if r.Picker == nil {
		t.Fatal("Picker is nil")
	}
	pk := r.Picker
	checks := []struct {
		name string
		ok   bool
	}{
		{"ID", r.ID == resultID},
		{"Title", r.Title == title},
		{"Score", r.Score == TriggerScore},
		{"Category", r.Category == providers.CatEmoji},
		{"Icon", r.Icon.ThemeName == iconName},
		{"Action zero", r.Action.Kind == ""},
		{"AltAction nil", r.AltAction == nil},
		{"Form nil", r.Form == nil},
		{"Columns default", pk.Columns == 0},
		{"Query", pk.Query == "smile"},
		{"Items count", len(pk.Items) > 1500},
		{"first value", len(pk.Items) > 0 && pk.Items[0].Value == "😀"},
		{"first label", len(pk.Items) > 0 && pk.Items[0].Label == "grinning face"},
		{"first keywords", len(pk.Items) > 0 && slices.Contains(pk.Items[0].Keywords, "grin")},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			if !c.ok {
				t.Errorf("%s check failed: %+v", c.name, r)
			}
		})
	}

	t.Run("shared backing", func(t *testing.T) {
		g2, err := p.Query(context.Background(), "emo")
		if err != nil || len(g2) != 1 || g2[0].Picker == nil {
			t.Fatalf("second query = %v, %v", g2, err)
		}
		if &g2[0].Picker.Items[0] != &pk.Items[0] {
			t.Error("second query does not share the item slice")
		}
		g3, err := New(fuzzy.Score).Query(context.Background(), "emo")
		if err != nil || len(g3) != 1 || g3[0].Picker == nil {
			t.Fatalf("new provider query = %v, %v", g3, err)
		}
		if &g3[0].Picker.Items[0] != &pk.Items[0] {
			t.Error("second provider does not share the item slice")
		}
	})
}

func TestQueryCancelled(t *testing.T) {
	p := New(fuzzy.Score)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, q := range []string{"emo", "picker", ""} {
		t.Run(q, func(t *testing.T) {
			got, err := p.Query(ctx, q)
			if !errors.Is(err, context.Canceled) || got != nil {
				t.Fatalf("Query(%q) = (%v, %v), want (nil, context.Canceled)", q, got, err)
			}
		})
	}
}

func hasSkinTone(s string) bool {
	for _, r := range s {
		if r >= 0x1F3FB && r <= 0x1F3FF {
			return true
		}
	}
	return false
}

func findBase(items []providers.PickerItem, v string) *providers.PickerItem {
	for i := range items {
		if items[i].Value == v {
			return &items[i]
		}
	}
	return nil
}

func TestDatasetInvariants(t *testing.T) {
	items := dataset()
	variantCount := func(v string, min, max int, contains string) func(t *testing.T) {
		return func(t *testing.T) {
			b := findBase(items, v)
			if b == nil {
				t.Fatalf("base %q missing", v)
			}
			n := len(b.Variants)
			if n < min || (max >= 0 && n > max) {
				t.Errorf("%q has %d variants, want [%d, %d]", v, n, min, max)
			}
			if contains != "" && !slices.ContainsFunc(b.Variants, func(it providers.PickerItem) bool { return it.Value == contains }) {
				t.Errorf("%q variants missing %q", v, contains)
			}
		}
	}
	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"values valid", func(t *testing.T) {
			for _, b := range items {
				for _, it := range append([]providers.PickerItem{b}, b.Variants...) {
					if it.Value == "" || !utf8.ValidString(it.Value) {
						t.Errorf("bad value %q", it.Value)
					}
					if it.Label == "" {
						t.Errorf("empty label for %q", it.Value)
					}
				}
			}
		}},
		{"skin tones only on variants", func(t *testing.T) {
			for _, b := range items {
				if hasSkinTone(b.Value) {
					t.Errorf("base %q carries a skin tone", b.Value)
				}
				for _, v := range b.Variants {
					if !hasSkinTone(v.Value) {
						t.Errorf("variant %q of %q has no skin tone", v.Value, b.Value)
					}
				}
			}
		}},
		{"unique values", func(t *testing.T) {
			seen := map[string]bool{}
			for _, b := range items {
				for _, it := range append([]providers.PickerItem{b}, b.Variants...) {
					if seen[it.Value] {
						t.Errorf("duplicate value %q", it.Value)
					}
					seen[it.Value] = true
				}
			}
		}},
		{"keyword coverage", func(t *testing.T) {
			n := 0
			for _, b := range items {
				if len(b.Keywords) > 0 {
					n++
				}
			}
			if n*100 < len(items)*95 {
				t.Errorf("%d/%d bases have keywords, want >= 95%%", n, len(items))
			}
		}},
		{"waving hand", variantCount("👋", 5, 5, "")},
		{"index pointing up", variantCount("☝️", 5, 5, "")},
		{"couple with heart", variantCount("💑", 25, -1, "🧑🏻‍❤️‍🧑🏼")},
		{"kiss", variantCount("💏", 25, -1, "")},
		{"special bases present", func(t *testing.T) {
			for _, v := range []string{"#️⃣", "🇺🇸", "🏴󠁧󠁢󠁥󠁮󠁧󠁿"} {
				if findBase(items, v) == nil {
					t.Errorf("base %q missing", v)
				}
			}
		}},
		{"no component bases", func(t *testing.T) {
			for _, v := range []string{"🏻", "🦰"} {
				if findBase(items, v) != nil {
					t.Errorf("component %q listed as a base", v)
				}
			}
			for _, b := range items {
				if strings.Contains(b.Label, "skin tone") {
					t.Errorf("base %q label %q mentions skin tone", b.Value, b.Label)
				}
			}
		}},
		{"base count", func(t *testing.T) {
			if n := len(items); n < 1700 || n > 2400 {
				t.Errorf("%d bases, want [1700, 2400]", n)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.fn)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []providers.PickerItem
	}{
		{
			name: "comments and blanks skipped",
			data: "# header\n\nA\ta\tx|y\n# mid\n\n",
			want: []providers.PickerItem{{Value: "A", Label: "a", Keywords: []string{"x", "y"}}},
		},
		{
			name: "keycap base is not a comment",
			data: "#\n# c\n#\uFE0F\u20E3\tkeycap: #\tkeycap\n",
			want: []providers.PickerItem{{Value: "#\uFE0F\u20E3", Label: "keycap: #", Keywords: []string{"keycap"}}},
		},
		{
			name: "variant before base skipped",
			data: "\tV\tv\nA\ta\tk\n",
			want: []providers.PickerItem{{Value: "A", Label: "a", Keywords: []string{"k"}}},
		},
		{
			name: "two-field base",
			data: "A\ta\n",
			want: []providers.PickerItem{{Value: "A", Label: "a"}},
		},
		{
			name: "short line skipped",
			data: "A\nB\tb\t\n",
			want: []providers.PickerItem{{Value: "B", Label: "b"}},
		},
		{
			name: "crlf",
			data: "A\ta\tk\r\n\tV\tv\r\n",
			want: []providers.PickerItem{{Value: "A", Label: "a", Keywords: []string{"k"},
				Variants: []providers.PickerItem{{Value: "V", Label: "v"}}}},
		},
		{
			name: "empty keyword field",
			data: "A\ta\t\n",
			want: []providers.PickerItem{{Value: "A", Label: "a"}},
		},
		{
			name: "empty variant value skipped",
			data: "A\ta\tk\n\t\tv\n\tV\tv\n",
			want: []providers.PickerItem{{Value: "A", Label: "a", Keywords: []string{"k"},
				Variants: []providers.PickerItem{{Value: "V", Label: "v"}}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parse(tt.data)
			if !slices.EqualFunc(got, tt.want, itemEqual) {
				t.Fatalf("parse(%q) = %+v, want %+v", tt.data, got, tt.want)
			}
		})
	}
}

func itemEqual(a, b providers.PickerItem) bool {
	return a.Value == b.Value && a.Label == b.Label &&
		slices.Equal(a.Keywords, b.Keywords) && (a.Keywords == nil) == (b.Keywords == nil) &&
		slices.EqualFunc(a.Variants, b.Variants, itemEqual)
}
