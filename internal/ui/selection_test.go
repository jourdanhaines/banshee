package ui

import "testing"

func TestSelectionReset(t *testing.T) {
	tests := []struct {
		name  string
		start int // pre-existing index (-1 for none)
		count int
		n     int
		want  int
	}{
		{"empty list selects nothing", -1, 0, 0, -1},
		{"non-empty list selects the top hit", -1, 0, 5, 0},
		{"reset re-selects the top hit", 3, 5, 5, 0},
		{"shrinking to empty deselects", 3, 5, 0, -1},
		{"negative count treated as empty", 0, 3, -1, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Selection{index: tt.start, count: tt.count}
			if got := s.Reset(tt.n); got != tt.want {
				t.Errorf("Reset(%d) = %d, want %d", tt.n, got, tt.want)
			}
			if got := s.Index(); got != tt.want {
				t.Errorf("Index() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSelectionMove(t *testing.T) {
	tests := []struct {
		name        string
		start       int
		count       int
		delta       int
		want        int
		wantChanged bool
	}{
		{"down from the top", 0, 5, 1, 1, true},
		{"up from the middle", 3, 5, -1, 2, true},
		{"down clamps at the last row", 4, 5, 1, 4, false},
		{"up clamps at the first row", 0, 5, -1, 0, false},
		{"single row list never moves", 0, 1, 1, 0, false},
		{"empty list stays deselected", -1, 0, 1, -1, false},
		{"unselected + down lands on the first row", -1, 5, 1, 0, true},
		{"unselected + up lands on the last row", -1, 5, -1, 4, true},
		{"large delta clamps down", 1, 5, 99, 4, true},
		{"large delta clamps up", 3, 5, -99, 0, true},
		{"zero delta changes nothing", 2, 5, 0, 2, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Selection{index: tt.start, count: tt.count}
			changed := s.Move(tt.delta)
			if changed != tt.wantChanged {
				t.Errorf("Move(%d) changed = %v, want %v", tt.delta, changed, tt.wantChanged)
			}
			if got := s.Index(); got != tt.want {
				t.Errorf("Move(%d) → Index() = %d, want %d", tt.delta, got, tt.want)
			}
		})
	}
}

func TestSelectionSet(t *testing.T) {
	tests := []struct {
		name        string
		start       int
		count       int
		set         int
		want        int
		wantChanged bool
	}{
		{"in range", 0, 5, 3, 3, true},
		{"same index", 3, 5, 3, 3, false},
		{"past the end clamps", 0, 5, 99, 4, true},
		{"negative clamps to the first row", 3, 5, -5, 0, true},
		{"empty list deselects", 0, 0, 2, -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Selection{index: tt.start, count: tt.count}
			if changed := s.Set(tt.set); changed != tt.wantChanged {
				t.Errorf("Set(%d) changed = %v, want %v", tt.set, changed, tt.wantChanged)
			}
			if got := s.Index(); got != tt.want {
				t.Errorf("Set(%d) → Index() = %d, want %d", tt.set, got, tt.want)
			}
		})
	}
}

func TestSelectionValid(t *testing.T) {
	s := NewSelection()
	if s.Valid() {
		t.Error("a fresh selection reports Valid() = true")
	}
	s.Reset(3)
	if !s.Valid() {
		t.Error("after Reset(3), Valid() = false")
	}
	s.Reset(0)
	if s.Valid() {
		t.Error("after Reset(0), Valid() = true")
	}
}

// TestSelectionKeyboardWalk exercises the sequence a user actually produces:
// query → results → several Ctrl-J presses past the end → Ctrl-K back up.
func TestSelectionKeyboardWalk(t *testing.T) {
	s := NewSelection()
	s.Reset(3)

	seen := []int{s.Index()}
	for i := 0; i < 4; i++ { // one more Down than there are rows
		s.Move(1)
		seen = append(seen, s.Index())
	}
	for i := 0; i < 4; i++ {
		s.Move(-1)
		seen = append(seen, s.Index())
	}

	want := []int{0, 1, 2, 2, 2, 1, 0, 0, 0}
	if len(seen) != len(want) {
		t.Fatalf("walk = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("walk = %v, want %v", seen, want)
		}
	}
}
