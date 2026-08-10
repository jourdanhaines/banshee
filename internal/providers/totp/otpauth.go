package totp

import (
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// base32NoPad is the alphabet TOTP seeds are shared in. Padding is optional in
// practice — QR-code exporters disagree about it — so the decoder strips '='
// and runs unpadded rather than rejecting either spelling.
var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// ParseSecretInput accepts either form a user can plausibly paste into the
// "secret" field and returns everything needed to store an entry: the
// normalized base32 seed, the algorithm parameters, and the issuer/account
// hints an otpauth URI carries (empty for a raw seed).
//
// Accepted inputs:
//
//   - a raw base32 seed, case-insensitive, with arbitrary internal spacing and
//     optional padding ("jbsw y3dp ehpk 3pxp");
//   - a full otpauth://totp/... URI as produced by a "show me the secret" link
//     or a scanned QR code.
//
// The seed is returned as text rather than bytes because that is what goes
// into the secrets Store; decode it with DecodeSecret at use time.
func ParseSecretInput(s string) (secretB32 string, p Params, issuer, account string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", Params{}, "", "", errors.New("totp: empty secret")
	}
	if isOTPAuth(s) {
		return parseURI(s)
	}

	secretB32, err = normalizeSecret(s)
	if err != nil {
		return "", Params{}, "", "", err
	}
	return secretB32, DefaultParams(), "", "", nil
}

// DecodeSecret turns a base32 seed into the raw bytes Code needs, applying the
// same normalization as ParseSecretInput so a seed that was accepted at add
// time can never fail at copy time.
func DecodeSecret(b32 string) ([]byte, error) {
	norm, err := normalizeSecret(b32)
	if err != nil {
		return nil, err
	}
	return base32NoPad.DecodeString(norm)
}

// isOTPAuth reports whether s looks like an otpauth URI. The scheme is matched
// case-insensitively because URI schemes are, and exporters are inconsistent.
func isOTPAuth(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "otpauth://")
}

// normalizeSecret upper-cases, drops whitespace and padding, and verifies the
// result decodes. Validating here means a bad seed is refused while the user
// is still looking at the form, not silently stored and discovered broken on
// the first copy.
func normalizeSecret(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '=':
			continue
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		default:
			b.WriteRune(r)
		}
	}
	norm := b.String()
	if norm == "" {
		return "", errors.New("totp: empty secret")
	}
	raw, err := base32NoPad.DecodeString(norm)
	if err != nil {
		// The error text never quotes the input: a rejected seed is still
		// secret material and must not reach a log or a notification.
		return "", fmt.Errorf("totp: secret is not valid base32: %w", err)
	}
	// Go's unpadded decoder tolerates a trailing partial group, so a
	// one-character seed decodes to zero bytes instead of failing. Catch it
	// here, because Code would otherwise reject the stored entry at copy
	// time — long after the user could still fix the paste.
	if len(raw) == 0 {
		return "", errors.New("totp: secret is too short to be a valid base32 seed")
	}
	// The same tolerance silently drops a trailing partial group anywhere
	// past the first block: a seed clipped to a length of 1, 3 or 6 mod 8
	// decodes without error and loses its last characters, so the entry is
	// stored happily and mints wrong codes forever with nothing to diagnose.
	// No base32 encoding of n bytes has those lengths, so requiring the
	// round trip to agree rejects exactly the truncated pastes.
	if base32NoPad.EncodedLen(len(raw)) != len(norm) {
		return "", errors.New("totp: secret is not a complete base32 seed — it looks truncated")
	}
	return norm, nil
}

// parseURI reads an otpauth://totp/ URI per the Key Uri Format.
func parseURI(s string) (string, Params, string, string, error) {
	u, err := url.Parse(s)
	if err != nil {
		// The *url.Error is deliberately discarded rather than wrapped: its
		// Error() quotes the whole input URI, secret= seed and all, and this
		// error is rendered straight into a desktop notification and the
		// daemon log by ui.submitForm. Same rule as normalizeSecret above.
		return "", Params{}, "", "", errors.New("totp: malformed otpauth URI")
	}
	switch strings.ToLower(u.Host) {
	case "totp":
	case "hotp":
		// HOTP is counter-based: it has no clock, and copying a code
		// advances shared state on the server. banshee has nowhere to
		// keep that counter, so refuse rather than mint dead codes.
		return "", Params{}, "", "", errors.New("totp: counter-based otpauth://hotp URIs are not supported, only otpauth://totp")
	default:
		return "", Params{}, "", "", fmt.Errorf("totp: unsupported otpauth type %q", u.Host)
	}

	q := u.Query()

	secretB32, err := normalizeSecret(q.Get("secret"))
	if err != nil {
		return "", Params{}, "", "", err
	}

	// Label is "Issuer:account" or bare "account"; url.Parse has already
	// percent-decoded it, so a "%3A"-encoded colon reads the same as a
	// literal one.
	label := strings.TrimPrefix(u.Path, "/")
	issuer, account := "", strings.TrimSpace(label)
	if pre, rest, ok := strings.Cut(label, ":"); ok {
		issuer, account = strings.TrimSpace(pre), strings.TrimSpace(rest)
	}
	// The issuer parameter is authoritative over the label prefix: the Key
	// Uri Format recommends emitting both, and only the parameter survives
	// label mangling by exporters.
	if v := strings.TrimSpace(q.Get("issuer")); v != "" {
		issuer = v
	}

	p := DefaultParams()
	if v := q.Get("algorithm"); strings.TrimSpace(v) != "" {
		p.Algorithm = normalizeAlgorithm(v)
	}
	if v := q.Get("digits"); strings.TrimSpace(v) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return "", Params{}, "", "", fmt.Errorf("totp: bad digits %q in otpauth URI", v)
		}
		p.Digits = n
	}
	if v := q.Get("period"); strings.TrimSpace(v) != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return "", Params{}, "", "", fmt.Errorf("totp: bad period %q in otpauth URI", v)
		}
		p.Period = n
	}
	if err := p.validate(); err != nil {
		return "", Params{}, "", "", err
	}
	return secretB32, p, issuer, account, nil
}
