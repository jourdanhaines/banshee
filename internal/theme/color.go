package theme

import (
	"fmt"
	"strconv"
	"strings"
)

// RGB is an 8-bit-per-channel color parsed from a CSS hex literal. It exists
// so the theme can derive translucent variants (borders, selection bars,
// badge backgrounds) from the single accent color the user configures.
type RGB struct {
	R, G, B uint8
}

// ParseHexColor parses a CSS hex color. Accepted forms are "#rgb", "#rrggbb",
// "#rgba" and "#rrggbbaa", with or without the leading '#'; any alpha channel
// is parsed for validation but discarded, because banshee derives its own
// alphas. ok is false when s is not a hex color, in which case the caller
// should fall back to the default accent.
func ParseHexColor(s string) (c RGB, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if !isHex(s) {
		return RGB{}, false
	}
	switch len(s) {
	case 3, 4: // #rgb / #rgba — each nibble doubled, per CSS
		r := nibble(s[0])
		g := nibble(s[1])
		b := nibble(s[2])
		return RGB{R: r * 17, G: g * 17, B: b * 17}, true
	case 6, 8: // #rrggbb / #rrggbbaa
		v, err := strconv.ParseUint(s[:6], 16, 32)
		if err != nil {
			return RGB{}, false
		}
		return RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}, true
	default:
		return RGB{}, false
	}
}

// List renders the color as "r, g, b" for interpolation into a CSS rgba()
// literal, e.g. rgba(122, 162, 247, 0.35).
func (c RGB) List() string {
	return fmt.Sprintf("%d, %d, %d", c.R, c.G, c.B)
}

// Hex renders the color as a lowercase "#rrggbb" literal.
func (c RGB) Hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if nibbleOK(s[i]) {
			continue
		}
		return false
	}
	return true
}

func nibbleOK(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func nibble(b byte) uint8 {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	default:
		return b - 'A' + 10
	}
}

// formatAlpha renders a float alpha with at most three decimals and no
// trailing zeros, so the generated CSS stays readable and stable.
func formatAlpha(a float64) string {
	if a < 0 {
		a = 0
	}
	if a > 1 {
		a = 1
	}
	s := strconv.FormatFloat(a, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" {
		s = "0"
	}
	return s
}
