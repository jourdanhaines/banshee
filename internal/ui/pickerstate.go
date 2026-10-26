package ui

import (
	"sort"
	"strings"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// DefaultPickerColumns is the grid width used when Picker.Columns is unset.
const DefaultPickerColumns = 12

// PickerColumns returns the grid width for p: its Columns, or
// DefaultPickerColumns when the provider left it unset (or set it nonsensically).
func PickerColumns(p providers.Picker) int {
	if p.Columns <= 0 {
		return DefaultPickerColumns
	}
	return p.Columns
}

// GridDir is an arrow-key direction over the picker grid.
type GridDir int

const (
	GridUp GridDir = iota
	GridDown
	GridLeft
	GridRight
)

// FilterPicker returns the indices of items matching q, best first. A blank
// q returns every index in display order (never nil). Otherwise q is split on
// whitespace and every token must match an item's Label or one of its
// Keywords; a token scores its best match, an item the sum over tokens, and
// ties keep display order. Variants are never searched.
func FilterPicker(items []providers.PickerItem, q string, score fuzzy.Scorer) []int {
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		all := make([]int, len(items))
		for i := range items {
			all[i] = i
		}
		return all
	}

	// fuzzy.Score is a subsequence matcher, so "face grin" would fail against
	// "grinning face" as one string; tokens are matched independently instead.
	type hit struct{ idx, score int }
	hits := []hit{}
	for i, it := range items {
		total := 0
		matched := true
		for _, tok := range tokens {
			best, ok := bestScore(tok, it, score)
			if !ok {
				matched = false
				break
			}
			total += best
		}
		if matched {
			hits = append(hits, hit{i, total})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })

	out := make([]int, len(hits))
	for i, h := range hits {
		out[i] = h.idx
	}
	return out
}

// bestScore is token's best score over the item's Label and Keywords.
func bestScore(token string, it providers.PickerItem, score fuzzy.Scorer) (int, bool) {
	best, found := 0, false
	consider := func(c string) {
		if s, ok := score(token, c); ok && (!found || s > best) {
			best, found = s, true
		}
	}
	consider(it.Label)
	for _, kw := range it.Keywords {
		consider(kw)
	}
	return best, found
}

// MoveHighlight returns the cell highlighted after moving dir from cur in a
// grid of n cells laid out cols wide. -1 means "highlight in the entry": no
// cell is highlighted and Enter falls back to the first visible cell. Up from
// the top row returns to the entry when allowNone, otherwise it stays put.
//
// Like Selection, the grid never wraps: Left/Right walk reading order and
// clamp at the ends, and Down from a row with no cell beneath clamps to the
// last cell when a shorter later row exists.
func MoveHighlight(cur, n, cols int, dir GridDir, allowNone bool) int {
	if n <= 0 {
		return -1
	}
	if cur >= n {
		cur = n - 1
	}
	if cols < 1 {
		cols = 1
	}
	if cur < 0 {
		switch dir {
		case GridDown, GridRight:
			return 0
		default:
			if allowNone {
				return -1
			}
			return 0
		}
	}
	switch dir {
	case GridLeft:
		return max(cur-1, 0)
	case GridRight:
		return min(cur+1, n-1)
	case GridUp:
		if cur-cols >= 0 {
			return cur - cols
		}
		if allowNone {
			return -1
		}
		return cur
	case GridDown:
		if cur+cols < n {
			return cur + cols
		}
		if cur/cols < (n-1)/cols {
			return n - 1
		}
		return cur
	}
	return cur
}

// PickerState is the display-free model of an open picker page: which items
// are visible under the current filter and which visible cell is highlighted.
// Highlight indexes Visible, not Items; -1 means the entry holds the highlight.
type PickerState struct {
	Items     []providers.PickerItem
	Cols      int
	Visible   []int
	Highlight int
}

// NewPickerState returns a state with every item visible. The highlight
// starts in the entry when allowNone, else on the first cell (-1 when empty).
func NewPickerState(items []providers.PickerItem, cols int, allowNone bool) PickerState {
	s := PickerState{Items: items, Cols: cols, Visible: FilterPicker(items, "", nil), Highlight: -1}
	if !allowNone && len(items) > 0 {
		s.Highlight = 0
	}
	return s
}

// Filter re-runs FilterPicker over q and returns the highlight to the entry,
// since the previously highlighted cell may no longer be visible.
func (s *PickerState) Filter(q string, score fuzzy.Scorer) {
	s.Visible = FilterPicker(s.Items, q, score)
	s.Highlight = -1
}

// Move applies MoveHighlight and reports whether the highlight changed, so
// the caller can skip a redundant redraw and scroll.
func (s *PickerState) Move(dir GridDir, allowNone bool) bool {
	next := MoveHighlight(s.Highlight, len(s.Visible), s.Cols, dir, allowNone)
	changed := next != s.Highlight
	s.Highlight = next
	return changed
}

// TargetIndex is the visible index Enter acts on: the highlight when valid,
// else the first visible cell, else -1.
func (s *PickerState) TargetIndex() int {
	if s.Highlight >= 0 && s.Highlight < len(s.Visible) {
		return s.Highlight
	}
	if len(s.Visible) > 0 {
		return 0
	}
	return -1
}

// Target returns the item Enter acts on, and false when nothing is visible.
func (s *PickerState) Target() (providers.PickerItem, bool) {
	i := s.TargetIndex()
	if i < 0 {
		return providers.PickerItem{}, false
	}
	return s.Items[s.Visible[i]], true
}
