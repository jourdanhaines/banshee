package ui

// Selection tracks which result row is highlighted. It is deliberately
// separate from GTK: the launcher drives ListBox.SelectRow from this state
// (the entry keeps keyboard focus, so GTK's own key navigation never runs)
// and unit tests can exercise the index math directly.
//
// Selection does not wrap: pressing Down on the last row keeps the last row
// selected, matching the "clamp" behaviour of the fzf picker banshee replaces.
type Selection struct {
	index int // -1 when there is nothing to select
	count int
}

// NewSelection returns an empty Selection (no rows, nothing selected).
func NewSelection() Selection { return Selection{index: -1} }

// Index returns the selected row index, or -1 when the list is empty.
func (s *Selection) Index() int { return s.index }

// Count returns the number of rows the selection is defined over.
func (s *Selection) Count() int { return s.count }

// Valid reports whether Index points at a real row.
func (s *Selection) Valid() bool { return s.index >= 0 && s.index < s.count }

// Reset re-bases the selection onto a freshly rebuilt list of n rows and
// selects the first row (the top hit), which is what a new query generation
// should do. It returns the resulting index, -1 for an empty list.
func (s *Selection) Reset(n int) int {
	if n < 0 {
		n = 0
	}
	s.count = n
	if n == 0 {
		s.index = -1
	} else {
		s.index = 0
	}
	return s.index
}

// Move shifts the selection by delta (negative is up), clamped to the list.
// It reports whether the index actually changed, so the caller can skip a
// redundant SelectRow plus scroll.
func (s *Selection) Move(delta int) bool {
	if s.count == 0 {
		if s.index != -1 {
			s.index = -1
			return true
		}
		return false
	}
	next := s.index + delta
	if s.index < 0 {
		// Nothing selected yet: Down lands on the first row, Up on the last.
		if delta < 0 {
			next = s.count - 1
		} else {
			next = 0
		}
	}
	if next < 0 {
		next = 0
	}
	if next >= s.count {
		next = s.count - 1
	}
	if next == s.index {
		return false
	}
	s.index = next
	return true
}

// Set selects i explicitly (used when the pointer selects a row), clamping to
// the list. It reports whether the index changed.
func (s *Selection) Set(i int) bool {
	if s.count == 0 {
		i = -1
	} else if i < 0 {
		i = 0
	} else if i >= s.count {
		i = s.count - 1
	}
	if i == s.index {
		return false
	}
	s.index = i
	return true
}
