package ui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

func pickerFixture() []providers.PickerItem {
	return []providers.PickerItem{
		{Value: "😀", Label: "grinning face", Keywords: []string{"face", "grin", "smile"}},
		{Value: "😂", Label: "face with tears of joy", Keywords: []string{"laugh", "tears"}},
		{Value: "❤️", Label: "red heart", Keywords: []string{"love"}},
		{Value: "👍", Label: "thumbs up", Keywords: []string{"+1", "like", "yes"}, Variants: []providers.PickerItem{
			{Value: "👍🏻", Label: "thumbs up: light skin tone"},
			{Value: "👍🏿", Label: "thumbs up: dark skin tone"},
		}},
		{Value: "🐱", Label: "cat face", Keywords: []string{"cat", "pet"}},
	}
}

// containsScore scores a case-insensitive substring match, earlier is better.
func containsScore(q, c string) (int, bool) {
	idx := strings.Index(strings.ToLower(c), strings.ToLower(q))
	if idx < 0 {
		return 0, false
	}
	return 100 - idx, true
}

func constScore(string, string) (int, bool) { return 1, true }

func TestPickerColumns(t *testing.T) {
	tests := []struct {
		name string
		cols int
		want int
	}{
		{"unset uses default", 0, 12},
		{"negative uses default", -3, 12},
		{"explicit", 5, 5},
		{"explicit default", 12, 12},
		{"explicit eight", 8, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PickerColumns(providers.Picker{Columns: tt.cols}); got != tt.want {
				t.Errorf("PickerColumns(%d) = %d, want %d", tt.cols, got, tt.want)
			}
		})
	}
}

func TestFilterPicker(t *testing.T) {
	tests := []struct {
		name  string
		q     string
		score fuzzy.Scorer
		want  []int
	}{
		{"empty shows all", "", containsScore, []int{0, 1, 2, 3, 4}},
		{"whitespace shows all", "   ", containsScore, []int{0, 1, 2, 3, 4}},
		{"label match", "heart", containsScore, []int{2}},
		{"keyword match", "love", containsScore, []int{2}},
		{"best of label and keywords, ties keep order", "face", containsScore, []int{0, 1, 4}},
		{"tokens are ANDed", "cat pet", containsScore, []int{4}},
		{"tokens across label words", "face joy", containsScore, []int{1}},
		{"one token missing drops the item", "face love", containsScore, []int{}},
		{"no match", "zzz", containsScore, []int{}},
		{"equal scores keep display order", "a", constScore, []int{0, 1, 2, 3, 4}},
		{"variants are never searched", "light", containsScore, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterPicker(pickerFixture(), tt.q, tt.score)
			if got == nil {
				t.Fatalf("FilterPicker(%q) = nil, want non-nil", tt.q)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("FilterPicker(%q) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

func TestFilterPickerWithFuzzy(t *testing.T) {
	tests := []struct {
		name  string
		q     string
		check func([]int) bool
	}{
		{"prefix and keyword ranks first", "grin", func(got []int) bool { return len(got) > 0 && got[0] == 0 }},
		{"case-insensitive", "HEART", func(got []int) bool {
			for _, i := range got {
				if i == 2 {
					return true
				}
			}
			return false
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FilterPicker(pickerFixture(), tt.q, fuzzy.Score); !tt.check(got) {
				t.Errorf("FilterPicker(%q, fuzzy.Score) = %v", tt.q, got)
			}
		})
	}
}

func TestMoveHighlight(t *testing.T) {
	tests := []struct {
		name      string
		cur, n    int
		cols      int
		dir       GridDir
		allowNone bool
		want      int
	}{
		{"none down enters grid", -1, 20, 8, GridDown, true, 0},
		{"none right enters grid", -1, 20, 8, GridRight, true, 0},
		{"none up stays in entry", -1, 20, 8, GridUp, true, -1},
		{"none left stays in entry", -1, 20, 8, GridLeft, true, -1},
		{"none up without allowNone lands on first", -1, 20, 8, GridUp, false, 0},
		{"top row up returns to entry", 0, 20, 8, GridUp, true, -1},
		{"top row up without allowNone stays", 0, 20, 8, GridUp, false, 0},
		{"down one row", 3, 20, 8, GridDown, true, 11},
		{"up one row", 9, 20, 8, GridUp, true, 1},
		{"right crosses rows", 7, 20, 8, GridRight, true, 8},
		{"left crosses rows", 8, 20, 8, GridLeft, true, 7},
		{"left clamps at first", 0, 20, 8, GridLeft, true, 0},
		{"right clamps at last", 19, 20, 8, GridRight, true, 19},
		{"down into short last row", 11, 20, 8, GridDown, true, 19},
		{"down past short last row clamps to last", 15, 20, 8, GridDown, true, 19},
		{"down on last row stays", 17, 20, 8, GridDown, true, 17},
		{"empty grid", 0, 0, 8, GridDown, true, -1},
		{"zero cols treated as one", 2, 20, 0, GridDown, true, 3},
		{"stale cur clamps first", 30, 20, 8, GridLeft, true, 18},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MoveHighlight(tt.cur, tt.n, tt.cols, tt.dir, tt.allowNone); got != tt.want {
				t.Errorf("MoveHighlight(%d, %d, %d, %d, %v) = %d, want %d",
					tt.cur, tt.n, tt.cols, tt.dir, tt.allowNone, got, tt.want)
			}
		})
	}
}

func TestNewPickerState(t *testing.T) {
	tests := []struct {
		name      string
		items     []providers.PickerItem
		allowNone bool
		wantVis   []int
		wantHi    int
	}{
		{"allowNone starts in entry", pickerFixture(), true, []int{0, 1, 2, 3, 4}, -1},
		{"no allowNone starts on first cell", pickerFixture(), false, []int{0, 1, 2, 3, 4}, 0},
		{"empty items", nil, false, []int{}, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPickerState(tt.items, 8, tt.allowNone)
			if !reflect.DeepEqual(s.Visible, tt.wantVis) || s.Highlight != tt.wantHi || s.Cols != 8 {
				t.Errorf("NewPickerState = {Visible %v, Highlight %d, Cols %d}, want {%v, %d, 8}",
					s.Visible, s.Highlight, s.Cols, tt.wantVis, tt.wantHi)
			}
		})
	}
}

func TestPickerStateFilterResetsHighlight(t *testing.T) {
	tests := []struct {
		name    string
		q       string
		wantVis []int
	}{
		{"filter narrows", "face", []int{0, 1, 4}},
		{"filter clears", "", []int{0, 1, 2, 3, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPickerState(pickerFixture(), 8, false)
			s.Highlight = 2
			s.Filter(tt.q, containsScore)
			if !reflect.DeepEqual(s.Visible, tt.wantVis) || s.Highlight != -1 {
				t.Errorf("after Filter(%q): Visible %v, Highlight %d; want %v, -1", tt.q, s.Visible, s.Highlight, tt.wantVis)
			}
		})
	}
}

func TestPickerStateMoveReportsChange(t *testing.T) {
	tests := []struct {
		name        string
		start       int
		dir         GridDir
		wantHi      int
		wantChanged bool
	}{
		{"real move", 0, GridRight, 1, true},
		{"clamp at end", 4, GridRight, 4, false},
		{"clamp at start", 0, GridLeft, 0, false},
		{"entry to grid", -1, GridDown, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPickerState(pickerFixture(), 8, true)
			s.Highlight = tt.start
			changed := s.Move(tt.dir, true)
			if changed != tt.wantChanged || s.Highlight != tt.wantHi {
				t.Errorf("Move = %v, Highlight %d; want %v, %d", changed, s.Highlight, tt.wantChanged, tt.wantHi)
			}
		})
	}
}

func TestPickerStateTarget(t *testing.T) {
	tests := []struct {
		name      string
		highlight int
		items     []providers.PickerItem
		wantIdx   int
		wantOK    bool
	}{
		{"highlighted cell", 2, pickerFixture(), 2, true},
		{"entry falls back to first", -1, pickerFixture(), 0, true},
		{"nothing visible", -1, nil, -1, false},
		{"stale highlight falls back to first", 7, pickerFixture(), 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPickerState(tt.items, 8, true)
			s.Highlight = tt.highlight
			if got := s.TargetIndex(); got != tt.wantIdx {
				t.Errorf("TargetIndex() = %d, want %d", got, tt.wantIdx)
			}
			item, ok := s.Target()
			if ok != tt.wantOK {
				t.Fatalf("Target() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && item.Value != tt.items[s.Visible[tt.wantIdx]].Value {
				t.Errorf("Target() = %q, want %q", item.Value, tt.items[s.Visible[tt.wantIdx]].Value)
			}
		})
	}
}
