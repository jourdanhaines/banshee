// Package totp implements time-based one-time passwords (RFC 6238) and the
// non-secret metadata index banshee keeps beside them.
//
// The package is split three ways on purpose:
//
//   - rfc6238.go is the pure algorithm — stdlib only, no I/O, no clock of its
//     own. Every function takes the instant it should compute for, which is
//     what lets the tests replay the RFC 6238 Appendix B vectors and lets the
//     launcher recompute a code at dispatch time rather than at query time.
//   - otpauth.go turns whatever the user pasted (a raw base32 seed or a full
//     otpauth:// URI) into a normalized seed plus parameters.
//   - index.go stores names, issuers and parameters. It deliberately never
//     stores secret material: the seed lives in an internal/secrets Store
//     under the key totp/<name>, so the index can be a plain 0644 file that
//     survives a backend swap.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

// Hash algorithm names as they appear in an otpauth URI's "algorithm"
// parameter and in a stored Entry. They are exported so callers can build
// Params without stringly-typed guesswork; comparison is case-insensitive and
// tolerant of the hyphenated spelling ("SHA-256").
const (
	AlgSHA1   = "SHA1"
	AlgSHA256 = "SHA256"
	AlgSHA512 = "SHA512"
)

// Defaults mandated by RFC 6238 §5.2 and assumed by every authenticator app
// when an otpauth URI omits the corresponding parameter.
const (
	DefaultDigits = 6
	DefaultPeriod = 30
)

// maxDigits caps Digits at the width of the 31-bit dynamic-truncation value,
// beyond which extra digits would always be zero and only mislead the user.
const maxDigits = 10

// pow10 is the modulus table for dynamic truncation, indexed by digit count.
// uint64 because 10^10 overflows the uint32 the truncation itself produces.
var pow10 = [maxDigits + 1]uint64{
	1, 10, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
}

// Params is the per-entry tuning of the TOTP algorithm. It is a value type so
// a caller can copy a stored Entry's parameters and adjust one field without
// disturbing the entry.
type Params struct {
	// Digits is the length of the rendered code, 1..10.
	Digits int
	// Period is the code lifetime in seconds; it is also the counter step.
	Period int
	// Algorithm is the HMAC hash: AlgSHA1, AlgSHA256 or AlgSHA512.
	Algorithm string
}

// DefaultParams returns the parameters every authenticator assumes when an
// otpauth URI leaves them out: 6 digits, 30 seconds, SHA1.
func DefaultParams() Params {
	return Params{Digits: DefaultDigits, Period: DefaultPeriod, Algorithm: AlgSHA1}
}

// hashFor resolves p.Algorithm to a hash constructor. Unknown algorithms are
// an error rather than a silent fall back to SHA1: a wrong hash yields codes
// that look plausible but never validate, which is far harder to diagnose
// than a refusal at add time.
func (p Params) hashFor() (func() hash.Hash, error) {
	switch normalizeAlgorithm(p.Algorithm) {
	case AlgSHA1:
		return sha1.New, nil
	case AlgSHA256:
		return sha256.New, nil
	case AlgSHA512:
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("totp: unsupported algorithm %q (want SHA1, SHA256 or SHA512)", p.Algorithm)
	}
}

// validate rejects parameter sets that cannot produce a meaningful code.
func (p Params) validate() error {
	if p.Digits < 1 || p.Digits > maxDigits {
		return fmt.Errorf("totp: digits %d out of range (1..%d)", p.Digits, maxDigits)
	}
	if p.Period < 1 {
		return fmt.Errorf("totp: period %d must be positive", p.Period)
	}
	if _, err := p.hashFor(); err != nil {
		return err
	}
	return nil
}

// normalizeAlgorithm folds the spellings seen in the wild ("sha-256", "Sha256")
// onto the canonical constants.
func normalizeAlgorithm(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", " ", "", "_", "").Replace(s)
}

// Code returns the one-time password for secret at instant t.
//
// secret is the decoded seed (see DecodeSecret), not its base32 text. t is
// passed in rather than read from the clock so callers can compute a code at
// the moment they need it — the launcher renders a code when it builds a row
// and recomputes a fresh one when the user actually copies, which may be a
// rotation later.
func Code(secret []byte, t time.Time, p Params) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("totp: empty secret")
	}
	if err := p.validate(); err != nil {
		return "", err
	}
	newHash, err := p.hashFor()
	if err != nil {
		return "", err
	}

	counter := uint64(floorDiv(t.Unix(), int64(p.Period)))
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)

	mac := hmac.New(newHash, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 §5.3: the low nibble of the last byte
	// selects a 4-byte window, whose top bit is masked off so the value is
	// sign-agnostic across implementations.
	off := sum[len(sum)-1] & 0x0f
	bin := uint32(sum[off]&0x7f)<<24 |
		uint32(sum[off+1])<<16 |
		uint32(sum[off+2])<<8 |
		uint32(sum[off+3])

	return fmt.Sprintf("%0*d", p.Digits, uint64(bin)%pow10[p.Digits]), nil
}

// ExpiryOf returns the instant the code current at t stops being valid, i.e.
// the next period boundary. The launcher stores this on a result row so a
// display-free ticker can decide when to re-query without re-deriving any
// codes.
//
// A non-positive period falls back to DefaultPeriod so a half-populated entry
// yields a sane countdown instead of a division panic.
func ExpiryOf(t time.Time, period int) time.Time {
	if period < 1 {
		period = DefaultPeriod
	}
	step := int64(period)
	next := (floorDiv(t.Unix(), step) + 1) * step
	return time.Unix(next, 0).In(t.Location())
}

// Remaining reports how long the code current at t stays valid. It keeps t's
// sub-second part, so a caller that rounds up gets the countdown a user
// expects ("30s" for the first fraction of a period, never a flickering "29s").
func Remaining(t time.Time, period int) time.Duration {
	return ExpiryOf(t, period).Sub(t)
}

// FormatCode groups a code for display: "123456" reads as "123 456" far more
// reliably when typing it into another window. Only the display path uses it —
// the clipboard always carries the raw, ungrouped code, because most sites
// reject the spaces.
//
// Even lengths split in half (6→3+3, 8→4+4); odd lengths group by threes from
// the left. Codes of four characters or fewer are returned unchanged.
func FormatCode(code string) string {
	n := len(code)
	if n <= 4 {
		return code
	}
	if n%2 == 0 {
		return code[:n/2] + " " + code[n/2:]
	}
	var b strings.Builder
	for i := 0; i < n; i += 3 {
		if i > 0 {
			b.WriteByte(' ')
		}
		end := i + 3
		if end > n {
			end = n
		}
		b.WriteString(code[i:end])
	}
	return b.String()
}

// floorDiv divides rounding towards negative infinity. Go's / truncates
// towards zero, which would put every instant in the first period before the
// Unix epoch into the same counter bucket as the one after it.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
