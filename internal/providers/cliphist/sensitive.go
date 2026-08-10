package cliphist

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// MaskReasonHint is the MaskReason for captures the copying application
// itself marked sensitive (x-kde-passwordManagerHint / wl-copy --sensitive).
const MaskReasonHint = "password manager"

// hintMIME is the clipboard offer password managers add to sensitive copies.
// wl-copy --sensitive offers the same type, so banshee's own secret copies
// carry it too.
const hintMIME = "x-kde-passwordManagerHint"

// Token patterns that identify a secret regardless of entropy. Ordered
// roughly most-specific first; the first match names the reason.
var secretPatterns = []struct {
	reason string
	re     *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----`)},
	{"AWS access key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"GitHub token", regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b|\bgithub_pat_[A-Za-z0-9_]{20,}\b`)},
	{"API key", regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`)},
	{"JWT", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)},
}

// Generic secret-shaped token thresholds: one whitespace-free token, long
// enough, drawn from several character classes, with near-random entropy.
const (
	genericMinLen     = 20
	genericMinClasses = 3
	genericMinEntropy = 3.5 // bits per character
)

// LooksSecret reports whether text looks like secret material and, when it
// does, a short human reason for the row subtitle. It is a display heuristic,
// not a security boundary: a false negative leaves an entry unmasked in a
// list only the local user sees, and a false positive still copies fine.
func LooksSecret(text string) (bool, string) {
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			return true, p.reason
		}
	}

	token := strings.TrimSpace(text)
	if strings.ContainsAny(token, " \t\n\r") || len(token) < genericMinLen {
		return false, ""
	}
	// URLs are long single tokens with decent entropy, but they are what a
	// clipboard history exists to keep visible.
	if strings.Contains(token, "://") {
		return false, ""
	}
	if charClasses(token) < genericMinClasses {
		return false, ""
	}
	if shannonEntropy(token) <= genericMinEntropy {
		return false, ""
	}
	return true, "looks like a secret"
}

// charClasses counts which of lower/upper/digit/other appear in s.
func charClasses(s string) int {
	var lower, upper, digit, other bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	return n
}

// shannonEntropy returns the per-character entropy of s in bits.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	freq := make(map[rune]int)
	total := 0
	for _, r := range s {
		freq[r]++
		total++
	}
	var h float64
	for _, c := range freq {
		p := float64(c) / float64(total)
		h -= p * math.Log2(p)
	}
	return h
}

// maskTitle renders a sensitive entry's row title. Hinted captures are fully
// masked — the password manager asked for that. Heuristic matches keep their
// first three runes as a recognition handle. The bullet count is fixed so the
// mask never leaks the secret's length.
func maskTitle(e Entry) string {
	const bullets = "•••••"
	if e.MaskReason == MaskReasonHint {
		return "•••" + bullets
	}
	runes := []rune(strings.TrimSpace(e.Text))
	if len(runes) < 3 {
		return "•••" + bullets
	}
	return string(runes[:3]) + bullets
}
