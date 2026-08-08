package theme

import "testing"

func TestParseHexColor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want RGB
		ok   bool
	}{
		{"six digit with hash", "#7aa2f7", RGB{0x7a, 0xa2, 0xf7}, true},
		{"six digit without hash", "7aa2f7", RGB{0x7a, 0xa2, 0xf7}, true},
		{"uppercase", "#7AA2F7", RGB{0x7a, 0xa2, 0xf7}, true},
		{"surrounding space", "  #7aa2f7 ", RGB{0x7a, 0xa2, 0xf7}, true},
		{"three digit expands nibbles", "#abc", RGB{0xaa, 0xbb, 0xcc}, true},
		{"three digit white", "#fff", RGB{0xff, 0xff, 0xff}, true},
		{"four digit drops alpha", "#abcd", RGB{0xaa, 0xbb, 0xcc}, true},
		{"eight digit drops alpha", "#7aa2f780", RGB{0x7a, 0xa2, 0xf7}, true},
		{"black", "#000000", RGB{0, 0, 0}, true},
		{"empty", "", RGB{}, false},
		{"hash only", "#", RGB{}, false},
		{"named color", "rebeccapurple", RGB{}, false},
		{"non hex digit", "#gggggg", RGB{}, false},
		{"wrong length", "#12345", RGB{}, false},
		{"css function", "rgb(1,2,3)", RGB{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseHexColor(tt.in)
			if ok != tt.ok {
				t.Fatalf("ParseHexColor(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ParseHexColor(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestRGBFormatting(t *testing.T) {
	c := RGB{0x7a, 0xa2, 0xf7}
	if got, want := c.List(), "122, 162, 247"; got != want {
		t.Errorf("List() = %q, want %q", got, want)
	}
	if got, want := c.Hex(), "#7aa2f7"; got != want {
		t.Errorf("Hex() = %q, want %q", got, want)
	}
}

func TestFormatAlpha(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0.86, "0.86"},
		{0.35, "0.35"},
		{1, "1"},
		{0, "0"},
		{0.5, "0.5"},
		{0.125, "0.125"},
		{-1, "0"},         // clamped low
		{2.5, "1"},        // clamped high
		{0.1234, "0.123"}, // rounded to three decimals
	}
	for _, tt := range tests {
		if got := formatAlpha(tt.in); got != tt.want {
			t.Errorf("formatAlpha(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
