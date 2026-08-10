package totp

import (
	"testing"
	"time"
)

// The ASCII seeds from RFC 6238 Appendix B. The document specifies one 20-byte
// seed and says the SHA256/SHA512 variants repeat it up to the hash's block
// size, which is what the errata-corrected reference implementation does.
var (
	seedSHA1   = []byte("12345678901234567890")
	seedSHA256 = []byte("12345678901234567890123456789012")
	seedSHA512 = []byte("1234567890123456789012345678901234567890123456789012345678901234")
)

func seedFor(alg string) []byte {
	switch alg {
	case AlgSHA256:
		return seedSHA256
	case AlgSHA512:
		return seedSHA512
	default:
		return seedSHA1
	}
}

// TestCodeRFC6238Vectors replays every row of RFC 6238 Appendix B. A passing
// run is the proof that the counter derivation, the HMAC selection and the
// dynamic truncation are all correct; nothing else in the package needs to
// re-establish that.
func TestCodeRFC6238Vectors(t *testing.T) {
	tests := []struct {
		name string
		unix int64
		alg  string
		want string
	}{
		{"1970 sha1", 59, AlgSHA1, "94287082"},
		{"1970 sha256", 59, AlgSHA256, "46119246"},
		{"1970 sha512", 59, AlgSHA512, "90693936"},
		{"2005 sha1", 1111111109, AlgSHA1, "07081804"},
		{"2005 sha256", 1111111109, AlgSHA256, "68084774"},
		{"2005 sha512", 1111111109, AlgSHA512, "25091201"},
		{"2005 next step sha1", 1111111111, AlgSHA1, "14050471"},
		{"2005 next step sha256", 1111111111, AlgSHA256, "67062674"},
		{"2005 next step sha512", 1111111111, AlgSHA512, "99943326"},
		{"2009 sha1", 1234567890, AlgSHA1, "89005924"},
		{"2009 sha256", 1234567890, AlgSHA256, "91819424"},
		{"2009 sha512", 1234567890, AlgSHA512, "93441116"},
		{"2033 sha1", 2000000000, AlgSHA1, "69279037"},
		{"2033 sha256", 2000000000, AlgSHA256, "90698825"},
		{"2033 sha512", 2000000000, AlgSHA512, "38618901"},
		{"2603 sha1", 20000000000, AlgSHA1, "65353130"},
		{"2603 sha256", 20000000000, AlgSHA256, "77737706"},
		{"2603 sha512", 20000000000, AlgSHA512, "47863826"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Params{Digits: 8, Period: DefaultPeriod, Algorithm: tt.alg}
			got, err := Code(seedFor(tt.alg), time.Unix(tt.unix, 0).UTC(), p)
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Code = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCodeParams covers the parameters the RFC vectors hold fixed: shorter
// codes, a non-default period, and the leading-zero padding that a naive
// integer render would drop.
func TestCodeParams(t *testing.T) {
	tests := []struct {
		name   string
		secret []byte
		unix   int64
		params Params
		want   string
	}{
		{
			// The 6-digit truncation of the 8-digit "94287082" vector.
			name: "six digits", secret: seedSHA1, unix: 59,
			params: Params{Digits: 6, Period: 30, Algorithm: AlgSHA1},
			want:   "287082",
		},
		{
			// Leading zero: 6-digit form of "07081804" is "081804",
			// which must keep its zero to be typeable.
			name: "leading zero six digits", secret: seedSHA1, unix: 1111111109,
			params: Params{Digits: 6, Period: 30, Algorithm: AlgSHA1},
			want:   "081804",
		},
		{
			name: "leading zero eight digits", secret: seedSHA1, unix: 1111111109,
			params: Params{Digits: 8, Period: 30, Algorithm: AlgSHA1},
			want:   "07081804",
		},
		{
			// Counter 0 for a 60 s period spans t=0..59, so t=59 lands
			// on the same counter as t=0 does at the default period —
			// the RFC 4226 counter-0 value for this seed.
			name: "custom period", secret: seedSHA1, unix: 59,
			params: Params{Digits: 8, Period: 60, Algorithm: AlgSHA1},
			want:   "84755224",
		},
		{
			name: "custom period first step", secret: seedSHA1, unix: 0,
			params: Params{Digits: 8, Period: 30, Algorithm: AlgSHA1},
			want:   "84755224",
		},
		{
			name: "hyphenated algorithm accepted", secret: seedSHA256, unix: 59,
			params: Params{Digits: 8, Period: 30, Algorithm: "sha-256"},
			want:   "46119246",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Code(tt.secret, time.Unix(tt.unix, 0).UTC(), tt.params)
			if err != nil {
				t.Fatalf("Code: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Code = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodeErrors(t *testing.T) {
	tests := []struct {
		name   string
		secret []byte
		params Params
	}{
		{"empty secret", nil, DefaultParams()},
		{"unknown algorithm", seedSHA1, Params{Digits: 6, Period: 30, Algorithm: "MD5"}},
		{"zero params", seedSHA1, Params{}},
		{"zero digits", seedSHA1, Params{Digits: 0, Period: 30, Algorithm: AlgSHA1}},
		{"too many digits", seedSHA1, Params{Digits: 11, Period: 30, Algorithm: AlgSHA1}},
		{"non positive period", seedSHA1, Params{Digits: 6, Period: 0, Algorithm: AlgSHA1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Code(tt.secret, time.Unix(59, 0).UTC(), tt.params); err == nil {
				t.Fatalf("Code = %q, want error", got)
			}
		})
	}
}

func TestDefaultParams(t *testing.T) {
	got := DefaultParams()
	want := Params{Digits: 6, Period: 30, Algorithm: AlgSHA1}
	if got != want {
		t.Fatalf("DefaultParams = %+v, want %+v", got, want)
	}
}

func TestExpiryOfAndRemaining(t *testing.T) {
	tests := []struct {
		name          string
		at            time.Time
		period        int
		wantExpiry    int64
		wantRemaining time.Duration
	}{
		{"start of period", time.Unix(30, 0).UTC(), 30, 60, 30 * time.Second},
		{"mid period", time.Unix(45, 0).UTC(), 30, 60, 15 * time.Second},
		{"last second", time.Unix(59, 0).UTC(), 30, 60, time.Second},
		{"sub second kept", time.Unix(30, 500_000_000).UTC(), 30, 60, 29500 * time.Millisecond},
		{"custom period", time.Unix(100, 0).UTC(), 60, 120, 20 * time.Second},
		{"zero period falls back to default", time.Unix(45, 0).UTC(), 0, 60, 15 * time.Second},
		{"negative period falls back to default", time.Unix(45, 0).UTC(), -7, 60, 15 * time.Second},
		{"before epoch floors", time.Unix(-5, 0).UTC(), 30, 0, 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := ExpiryOf(tt.at, tt.period)
			if exp.Unix() != tt.wantExpiry {
				t.Fatalf("ExpiryOf = %d, want %d", exp.Unix(), tt.wantExpiry)
			}
			if got := Remaining(tt.at, tt.period); got != tt.wantRemaining {
				t.Fatalf("Remaining = %v, want %v", got, tt.wantRemaining)
			}
		})
	}
}

// TestExpiryOfBoundaryCode pins the property the launcher's ticker relies on:
// the code changes exactly at the instant ExpiryOf reports, not a second
// before or after.
func TestExpiryOfBoundaryCode(t *testing.T) {
	p := DefaultParams()
	at := time.Unix(1111111109, 0).UTC()
	exp := ExpiryOf(at, p.Period)

	before, err := Code(seedSHA1, exp.Add(-time.Nanosecond), p)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	now, err := Code(seedSHA1, at, p)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	after, err := Code(seedSHA1, exp, p)
	if err != nil {
		t.Fatalf("Code: %v", err)
	}
	if before != now {
		t.Fatalf("code changed before expiry: %q then %q", now, before)
	}
	if after == now {
		t.Fatalf("code %q did not change at expiry", after)
	}
}

func TestFormatCode(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{"six digits", "123456", "123 456"},
		{"eight digits", "12345678", "1234 5678"},
		{"seven digits", "1234567", "123 456 7"},
		{"leading zero preserved", "081804", "081 804"},
		{"short left alone", "1234", "1234"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatCode(tt.code); got != tt.want {
				t.Fatalf("FormatCode(%q) = %q, want %q", tt.code, got, tt.want)
			}
		})
	}
}
