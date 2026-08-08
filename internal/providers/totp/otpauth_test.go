package totp

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestParseSecretInput(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantSecret  string
		wantParams  Params
		wantIssuer  string
		wantAccount string
	}{
		{
			name: "raw base32 upper", in: "JBSWY3DPEHPK3PXP",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
		},
		{
			name: "raw base32 lowercase", in: "jbswy3dpehpk3pxp",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
		},
		{
			name: "raw base32 with inner spaces", in: "  jbsw y3dp ehpk 3pxp  ",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
		},
		{
			name: "raw base32 padded", in: "MFRGGZDFMZTWQ2LK===",
			wantSecret: "MFRGGZDFMZTWQ2LK", wantParams: DefaultParams(),
		},
		{
			name: "raw base32 unpadded odd length", in: "MZXW6YTB",
			wantSecret: "MZXW6YTB", wantParams: DefaultParams(),
		},
		{
			name: "uri bare account label", in: "otpauth://totp/alice@example.com?secret=JBSWY3DPEHPK3PXP",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantAccount: "alice@example.com",
		},
		{
			name: "uri issuer prefix in label", in: "otpauth://totp/GitHub:alice?secret=JBSWY3DPEHPK3PXP",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantIssuer: "GitHub", wantAccount: "alice",
		},
		{
			name: "uri encoded colon and spaces in label", in: "otpauth://totp/ACME%20Co%3Aalice%40example.com?secret=JBSWY3DPEHPK3PXP",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantIssuer: "ACME Co", wantAccount: "alice@example.com",
		},
		{
			name: "issuer param wins over label prefix", in: "otpauth://totp/Old:alice?secret=JBSWY3DPEHPK3PXP&issuer=New",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantIssuer: "New", wantAccount: "alice",
		},
		{
			name: "issuer param with no label prefix", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&issuer=GitHub",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantIssuer: "GitHub", wantAccount: "alice",
		},
		{
			name: "uri digits param", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&digits=8",
			wantSecret:  "JBSWY3DPEHPK3PXP",
			wantParams:  Params{Digits: 8, Period: 30, Algorithm: AlgSHA1},
			wantAccount: "alice",
		},
		{
			name: "uri period param", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&period=60",
			wantSecret:  "JBSWY3DPEHPK3PXP",
			wantParams:  Params{Digits: 6, Period: 60, Algorithm: AlgSHA1},
			wantAccount: "alice",
		},
		{
			name: "uri algorithm param", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&algorithm=SHA256",
			wantSecret:  "JBSWY3DPEHPK3PXP",
			wantParams:  Params{Digits: 6, Period: 30, Algorithm: AlgSHA256},
			wantAccount: "alice",
		},
		{
			name: "uri hyphenated lowercase algorithm", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&algorithm=sha-512",
			wantSecret:  "JBSWY3DPEHPK3PXP",
			wantParams:  Params{Digits: 6, Period: 30, Algorithm: AlgSHA512},
			wantAccount: "alice",
		},
		{
			name: "uri all params", in: "otpauth://totp/ACME:bob?secret=jbsw%20y3dp%20ehpk%203pxp&issuer=ACME&digits=8&period=15&algorithm=SHA256",
			wantSecret: "JBSWY3DPEHPK3PXP",
			wantParams: Params{Digits: 8, Period: 15, Algorithm: AlgSHA256},
			wantIssuer: "ACME", wantAccount: "bob",
		},
		{
			name: "uri scheme case insensitive", in: "OTPAUTH://TOTP/alice?secret=JBSWY3DPEHPK3PXP",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantAccount: "alice",
		},
		{
			name: "uri unknown params ignored", in: "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&image=https://x/y.png&counter=9",
			wantSecret: "JBSWY3DPEHPK3PXP", wantParams: DefaultParams(),
			wantAccount: "alice",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret, p, issuer, account, err := ParseSecretInput(tt.in)
			if err != nil {
				t.Fatalf("ParseSecretInput: %v", err)
			}
			if secret != tt.wantSecret {
				t.Errorf("secret = %q, want %q", secret, tt.wantSecret)
			}
			if p != tt.wantParams {
				t.Errorf("params = %+v, want %+v", p, tt.wantParams)
			}
			if issuer != tt.wantIssuer {
				t.Errorf("issuer = %q, want %q", issuer, tt.wantIssuer)
			}
			if account != tt.wantAccount {
				t.Errorf("account = %q, want %q", account, tt.wantAccount)
			}
		})
	}
}

func TestParseSecretInputErrors(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantSub string
	}{
		{"empty", "", "empty secret"},
		{"whitespace only", "   \t ", "empty secret"},
		{"invalid base32 characters", "not-a-secret!", "base32"},
		{"base32 digits outside alphabet", "01890189", "base32"},
		{"base32 decodes to nothing", "A", "too short"},
		// Go's unpadded decoder drops a trailing partial group without
		// complaining, so a clipped paste would otherwise be stored and mint
		// wrong codes forever. Lengths 1, 3 and 6 mod 8 are the three that no
		// real base32 encoding produces.
		{"truncated by one character", "JBSWY3DPEHPK3PXPA", "truncated"},
		{"truncated by three characters", "JBSWY3DPEHPK3PXPABC", "truncated"},
		{"truncated by six characters", "JBSWY3DPEHPK3PXPABCDEF", "truncated"},
		{"hotp rejected", "otpauth://hotp/alice?secret=JBSWY3DPEHPK3PXP&counter=0", "hotp"},
		{"unknown otpauth type", "otpauth://steam/alice?secret=JBSWY3DPEHPK3PXP", "unsupported otpauth type"},
		{"uri without secret", "otpauth://totp/alice?issuer=ACME", "empty secret"},
		{"uri bad secret", "otpauth://totp/alice?secret=!!!!", "base32"},
		{"uri bad digits", "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&digits=many", "bad digits"},
		{"uri out of range digits", "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&digits=99", "digits"},
		{"uri bad period", "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&period=soon", "bad period"},
		{"uri zero period", "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&period=0", "period"},
		{"uri unknown algorithm", "otpauth://totp/alice?secret=JBSWY3DPEHPK3PXP&algorithm=md5", "algorithm"},
		{"malformed uri", "otpauth://totp/%zz?secret=JBSWY3DPEHPK3PXP", "malformed otpauth"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret, _, _, _, err := ParseSecretInput(tt.in)
			if err == nil {
				t.Fatalf("ParseSecretInput = %q, want error", secret)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.wantSub)) {
				t.Fatalf("error %q does not mention %q", err, tt.wantSub)
			}
		})
	}
}

// TestParseSecretInputErrorsQuoteNoSecret pins the hygiene rule the package
// comment states: a rejected seed is still secret material, and this error text
// is rendered verbatim into a desktop notification and the daemon log by
// ui.submitForm. url.Parse in particular returns a *url.Error whose Error()
// embeds the whole input URI, secret= and all, so it must never be wrapped.
func TestParseSecretInputErrorsQuoteNoSecret(t *testing.T) {
	const seed = "JBSWY3DPEHPK3PXP"
	tests := []struct {
		name string
		in   string
	}{
		{"bad percent escape in the label", "otpauth://totp/Ex%ZZample:alice?secret=" + seed + "&issuer=Ex"},
		{"control character from a clipboard paste", "otpauth://totp/ACME\x7fCo:alice?secret=" + seed},
		{"newline from a line-wrapped copy", "otpauth://totp/ACME\nCo:alice?secret=" + seed},
		{"bad authority", "otpauth://totp:1x/alice?secret=" + seed},
		{"raw seed is not base32", seed + "!!!"},
		{"raw seed is truncated", seed + "ABC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _, _, err := ParseSecretInput(tt.in)
			if err == nil {
				t.Fatalf("ParseSecretInput = %q, want error", got)
			}
			if strings.Contains(err.Error(), seed) {
				t.Fatalf("error leaks the seed: %q", err)
			}
		})
	}
}

func TestDecodeSecret(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []byte
		wantErr bool
	}{
		{name: "upper", in: "MFRGGZDFMZTWQ2LK", want: []byte("abcdefghij")},
		{name: "lower", in: "mfrggzdfmztwq2lk", want: []byte("abcdefghij")},
		{name: "spaced", in: "MFRG GZDF MZTW Q2LK", want: []byte("abcdefghij")},
		{name: "padded", in: "MZXW6===", want: []byte("foo")},
		{name: "unpadded", in: "MZXW6", want: []byte("foo")},
		{name: "empty", in: "", wantErr: true},
		{name: "garbage", in: "!!!", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeSecret(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DecodeSecret = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeSecret: %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("DecodeSecret = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestParseThenCodeRoundTrip is the seam test: whatever ParseSecretInput
// accepts must survive DecodeSecret and produce the RFC vector, so an entry
// added through the form can never fail at copy time.
func TestParseThenCodeRoundTrip(t *testing.T) {
	// base32 of the RFC 6238 SHA1 seed "12345678901234567890".
	const rfcSeedB32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"raw", "gezd gnbv gy3t qojq gezd gnbv gy3t qojq", "287082"},
		{"uri", "otpauth://totp/ACME:alice?secret=" + rfcSeedB32 + "&digits=8", "94287082"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b32, p, _, _, err := ParseSecretInput(tt.in)
			if err != nil {
				t.Fatalf("ParseSecretInput: %v", err)
			}
			raw, err := DecodeSecret(b32)
			if err != nil {
				t.Fatalf("DecodeSecret: %v", err)
			}
			got, err := Code(raw, time.Unix(59, 0).UTC(), p)
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Code = %q, want %q", got, tt.want)
			}
		})
	}
}
