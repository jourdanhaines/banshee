package ui

import "testing"

func TestDeleteWordStart(t *testing.T) {
	tests := []struct {
		name string
		text string
		pos  int
		want int
	}{
		{"end of word", "foo bar", 7, 4},
		{"trailing space skipped first", "foo bar ", 8, 4},
		{"multiple trailing spaces", "foo bar   ", 10, 4},
		{"mid-word deletes to word start", "foobar", 3, 0},
		{"single word", "foo", 3, 0},
		{"all whitespace", "   ", 3, 0},
		{"empty", "", 0, 0},
		{"pos zero", "foo", 0, 0},
		{"pos beyond length clamps", "foo", 99, 0},
		{"negative pos clamps", "foo", -1, 0},
		{"multibyte runes", "héllo wörld", 11, 6},
		{"multibyte with trailing space", "héllo wörld ", 12, 6},
		{"cursor after space eats the word too", "a b", 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeleteWordStart(tt.text, tt.pos); got != tt.want {
				t.Errorf("DeleteWordStart(%q, %d) = %d, want %d", tt.text, tt.pos, got, tt.want)
			}
		})
	}
}
