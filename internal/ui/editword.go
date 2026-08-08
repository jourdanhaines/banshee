package ui

import "unicode"

// DeleteWordStart returns the character index where a Ctrl-W deletion of the
// word before pos begins, vim/readline style: skip any whitespace immediately
// left of the cursor, then the non-whitespace run before it. pos and the
// returned index are rune offsets — GTK's Editable API counts characters, not
// bytes. A pos out of range is clamped; a return equal to pos means there is
// nothing to delete.
func DeleteWordStart(text string, pos int) int {
	runes := []rune(text)
	if pos > len(runes) {
		pos = len(runes)
	}
	if pos < 0 {
		pos = 0
	}
	i := pos
	for i > 0 && unicode.IsSpace(runes[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(runes[i-1]) {
		i--
	}
	return i
}
