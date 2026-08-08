package ui

import (
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// StandardPeriod is the rotation window RFC 6238 authenticators use unless
// told otherwise. It is the assumed window for a live row that declares no
// Period, which is what lets rows produced before Result.Period existed still
// drain in step with everything else.
const StandardPeriod = 30 * time.Second

// IsStandard reports whether period is the ordinary 30-second TOTP window.
// Zero counts as standard: a live row that leaves Period unset means "the
// usual window" per the Result.Period contract. Standard rows are the ones the
// launcher can represent with a single shared bar, so this is the predicate
// that decides whether a row needs a bar of its own.
func IsStandard(period time.Duration) bool {
	return period == 0 || period == StandardPeriod
}

// Fraction is the share of a row's validity window still remaining at now: 1
// the instant the window opens, 0 once expiry has passed. It is what a
// progress bar's fraction is set to, so the bar drains rather than fills.
//
// The result is clamped to [0, 1] because neither end is under the UI's
// control — a provider may hand back a row whose expiry is further out than
// one period (a clock that jumped, a provider that padded the window), and a
// tick can land after expiry when the main loop was busy. A non-positive
// period falls back to StandardPeriod, matching IsStandard's reading of zero.
func Fraction(expiry time.Time, period time.Duration, now time.Time) float64 {
	if period <= 0 {
		period = StandardPeriod
	}
	rem := expiry.Sub(now)
	if rem <= 0 {
		return 0
	}
	if rem >= period {
		return 1
	}
	return float64(rem) / float64(period)
}

// StandardFraction is Fraction for a standard 30-second window, computed from
// the wall clock alone: every standard TOTP rotates on the unix%30 boundary, so
// the remaining share of the window is a property of the instant, not of any
// particular row. That is what lets one shared bar stand in for every standard
// row on screen without consulting them — and what keeps the bar honest across
// a query that lands mid-window.
func StandardFraction(now time.Time) float64 {
	// Go's % keeps the sign of the dividend, so a pre-epoch clock (a machine
	// that came up before NTP) would otherwise yield a fraction above 1 and a
	// bar GTK clamps silently; folding the offset back into [0, period) keeps
	// the result in (0, 1] for every instant.
	off := time.Duration(now.UnixNano()) % StandardPeriod
	if off < 0 {
		off += StandardPeriod
	}
	return float64(StandardPeriod-off) / float64(StandardPeriod)
}

// AnyLive reports whether rs contains a row whose content expires. The
// launcher starts its ticker only when this is true, so an ordinary query
// costs nothing extra.
func AnyLive(rs []providers.Result) bool {
	for i := range rs {
		if !rs[i].Expiry.IsZero() {
			return true
		}
	}
	return false
}

// AnyStandardLive reports whether rs contains a live row on the standard
// window. It is the shared bar's visibility condition: the bar speaks for
// standard-period rows only, so a result set of nothing but 60-second entries
// must leave it hidden and let those rows drain their own bars.
func AnyStandardLive(rs []providers.Result) bool {
	for i := range rs {
		if !rs[i].Expiry.IsZero() && IsStandard(rs[i].Period) {
			return true
		}
	}
	return false
}

// AnyExpired reports whether any live row in rs has reached its expiry
// instant at now. The launcher uses it as the trigger to re-run the current
// query: the bar can be drained in place, but the *content* behind it (a
// rotated TOTP code) can only come from the provider.
//
// The instant itself counts as expired, matching Fraction's clamp to 0.
func AnyExpired(rs []providers.Result, now time.Time) bool {
	for i := range rs {
		e := rs[i].Expiry
		if !e.IsZero() && !now.Before(e) {
			return true
		}
	}
	return false
}
