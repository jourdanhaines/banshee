package ui

import (
	"math"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// liveNow is a fixed instant so every case below is deterministic; the ticker
// itself reads the wall clock, but none of the logic under test does. It sits
// exactly on a 30-second boundary (every HH:MM:00 does, the Unix epoch being
// aligned to midnight), which is what makes the StandardFraction cases legible.
var liveNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// fracEps tolerates the float division in Fraction/StandardFraction; the
// values under test are ratios of durations, never exact binary fractions.
const fracEps = 1e-9

func closeTo(got, want float64) bool { return math.Abs(got-want) <= fracEps }

func TestIsStandard(t *testing.T) {
	tests := []struct {
		name   string
		period time.Duration
		want   bool
	}{
		{"zero means the usual window", 0, true},
		{"the standard window itself", 30 * time.Second, true},
		{"a 60-second window is not standard", 60 * time.Second, false},
		{"an odd window is not standard", 45 * time.Second, false},
		{"a negative window is not standard", -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsStandard(tt.period); got != tt.want {
				t.Errorf("IsStandard(%v) = %v, want %v", tt.period, got, tt.want)
			}
		})
	}
}

func TestFraction(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Time
		period time.Duration
		want   float64
	}{
		{"a full window is full", liveNow.Add(30 * time.Second), 30 * time.Second, 1},
		{"half a window", liveNow.Add(15 * time.Second), 30 * time.Second, 0.5},
		{"one second left of thirty", liveNow.Add(time.Second), 30 * time.Second, 1.0 / 30.0},
		{"the expiry instant is empty", liveNow, 30 * time.Second, 0},
		{"a past expiry is empty", liveNow.Add(-time.Second), 30 * time.Second, 0},
		{"a far-past expiry is empty", liveNow.Add(-99 * time.Hour), 30 * time.Second, 0},
		{"zero period defaults to standard", liveNow.Add(15 * time.Second), 0, 0.5},
		{"negative period defaults to standard", liveNow.Add(15 * time.Second), -time.Second, 0.5},
		{"half of a 60-second window", liveNow.Add(30 * time.Second), 60 * time.Second, 0.5},
		{"remaining longer than the period clamps", liveNow.Add(45 * time.Second), 30 * time.Second, 1},
		{"a nanosecond of life is not empty", liveNow.Add(time.Nanosecond), 30 * time.Second, 1.0 / 30e9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fraction(tt.expiry, tt.period, liveNow)
			if !closeTo(got, tt.want) {
				t.Errorf("Fraction(%v, %v, %v) = %v, want %v", tt.expiry, tt.period, liveNow, got, tt.want)
			}
		})
	}
}

func TestStandardFraction(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		{"on the boundary the window is whole", liveNow, 1},
		{"halfway through", liveNow.Add(15 * time.Second), 0.5},
		{"the second boundary of the minute", liveNow.Add(30 * time.Second), 1},
		{"halfway through the second window", liveNow.Add(45 * time.Second), 0.5},
		{"one second before the boundary", liveNow.Add(29 * time.Second), 1.0 / 30.0},
		{"a nanosecond before the boundary", liveNow.Add(30*time.Second - time.Nanosecond), 1.0 / 30e9},
		{"a nanosecond after the boundary is nearly whole", liveNow.Add(time.Nanosecond), 1 - 1.0/30e9},
		// A pre-epoch clock has a negative UnixNano; the window still runs from
		// 23:59:30 to 00:00:00, so these are the same halves as any other minute.
		{"pre-epoch, halfway through", time.Date(1969, 12, 31, 23, 59, 45, 0, time.UTC), 0.5},
		{"pre-epoch, ten seconds left", time.Date(1969, 12, 31, 23, 59, 50, 0, time.UTC), 1.0 / 3.0},
		{"pre-epoch, on the boundary", time.Date(1969, 12, 31, 23, 59, 30, 0, time.UTC), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StandardFraction(tt.now)
			if !closeTo(got, tt.want) {
				t.Errorf("StandardFraction(%v) = %v, want %v", tt.now, got, tt.want)
			}
			if got <= 0 || got > 1 {
				t.Errorf("StandardFraction(%v) = %v, outside (0, 1]", tt.now, got)
			}
		})
	}
}

// live builds a result carrying expiry; a zero expiry yields a static row.
func live(id string, expiry time.Time) providers.Result {
	return providers.Result{ID: id, Title: id, Expiry: expiry}
}

// livePeriod builds a live result on an explicit rotation window.
func livePeriod(id string, expiry time.Time, period time.Duration) providers.Result {
	r := live(id, expiry)
	r.Period = period
	return r
}

func TestAnyLive(t *testing.T) {
	tests := []struct {
		name string
		rs   []providers.Result
		want bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []providers.Result{}, false},
		{"all static", []providers.Result{live("a", time.Time{}), live("b", time.Time{})}, false},
		{"one live", []providers.Result{live("a", time.Time{}), live("b", liveNow.Add(time.Second))}, true},
		{"all live", []providers.Result{live("a", liveNow), live("b", liveNow.Add(time.Second))}, true},
		{"already expired still counts as live", []providers.Result{live("a", liveNow.Add(-time.Hour))}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyLive(tt.rs); got != tt.want {
				t.Errorf("AnyLive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnyStandardLive(t *testing.T) {
	exp := liveNow.Add(10 * time.Second)
	tests := []struct {
		name string
		rs   []providers.Result
		want bool
	}{
		{"nil slice", nil, false},
		{"static rows only", []providers.Result{live("a", time.Time{}), live("b", time.Time{})}, false},
		{"a live row with no period is standard", []providers.Result{live("a", exp)}, true},
		{"an explicit 30-second row is standard", []providers.Result{livePeriod("a", exp, 30*time.Second)}, true},
		{"only 60-second rows", []providers.Result{livePeriod("a", exp, 60*time.Second), livePeriod("b", exp, 60*time.Second)}, false},
		{"mixed periods", []providers.Result{livePeriod("a", exp, 60*time.Second), livePeriod("b", exp, 30*time.Second)}, true},
		{"a period on a static row is inert", []providers.Result{livePeriod("a", time.Time{}, 30*time.Second)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyStandardLive(tt.rs); got != tt.want {
				t.Errorf("AnyStandardLive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnyExpired(t *testing.T) {
	tests := []struct {
		name string
		rs   []providers.Result
		want bool
	}{
		{"nil slice", nil, false},
		{"static rows never expire", []providers.Result{live("a", time.Time{})}, false},
		{"future expiry", []providers.Result{live("a", liveNow.Add(time.Nanosecond))}, false},
		{"exact expiry instant counts", []providers.Result{live("a", liveNow)}, true},
		{"past expiry counts", []providers.Result{live("a", liveNow.Add(-time.Millisecond))}, true},
		{"one of many expired", []providers.Result{
			live("a", liveNow.Add(30*time.Second)),
			live("b", time.Time{}),
			live("c", liveNow.Add(-time.Second)),
		}, true},
		{"none of many expired", []providers.Result{
			live("a", liveNow.Add(30*time.Second)),
			live("b", time.Time{}),
			live("c", liveNow.Add(time.Second)),
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AnyExpired(tt.rs, liveNow); got != tt.want {
				t.Errorf("AnyExpired() = %v, want %v", got, tt.want)
			}
		})
	}
}
