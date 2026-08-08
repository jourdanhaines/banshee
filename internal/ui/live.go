package ui

import (
	"strconv"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// liveSeparator joins a row's own subtitle to its countdown. It matches the
// separator providers already use inside subtitles, so a live row reads as one
// sentence rather than two glued fragments.
const liveSeparator = " · "

// CountdownText renders the time left until expiry as whole seconds, e.g.
// "12s". The remainder is rounded *up* so a code that is still usable never
// reads "0s": the label only reaches "0s" once the instant has actually
// passed, which is the same boundary AnyExpired reports on. A past expiry is
// clamped rather than shown negative.
func CountdownText(expiry, now time.Time) string {
	rem := expiry.Sub(now)
	if rem <= 0 {
		return "0s"
	}
	secs := (rem + time.Second - 1) / time.Second // ceil
	return strconv.FormatInt(int64(secs), 10) + "s"
}

// LiveSubtitle is the subtitle text a live row shows at instant now: the
// provider's own subtitle with the countdown appended. It exists so the
// ticker and the initial row render produce byte-identical text — the label is
// seeded once at build time and then rewritten in place every second, and a
// mismatch between the two paths would show as a flicker on the first tick.
//
// A zero expiry means the row is static, so base is returned untouched.
func LiveSubtitle(base string, expiry, now time.Time) string {
	if expiry.IsZero() {
		return base
	}
	if base == "" {
		return CountdownText(expiry, now)
	}
	return base + liveSeparator + CountdownText(expiry, now)
}

// AnyLive reports whether rs contains a row whose content expires. The
// launcher starts its once-a-second ticker only when this is true, so an
// ordinary query costs nothing extra.
func AnyLive(rs []providers.Result) bool {
	for i := range rs {
		if !rs[i].Expiry.IsZero() {
			return true
		}
	}
	return false
}

// AnyExpired reports whether any live row in rs has reached its expiry
// instant at now. The launcher uses it as the trigger to re-run the current
// query: the countdown can be recomputed in place, but the *content* behind it
// (a rotated TOTP code) can only come from the provider.
//
// The instant itself counts as expired, matching CountdownText's "0s".
func AnyExpired(rs []providers.Result, now time.Time) bool {
	for i := range rs {
		e := rs[i].Expiry
		if !e.IsZero() && !now.Before(e) {
			return true
		}
	}
	return false
}
