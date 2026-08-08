package ui

import (
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// liveNow is a fixed instant so every case below is deterministic; the ticker
// itself reads the wall clock, but none of the logic under test does.
var liveNow = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func TestCountdownText(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Time
		want   string
	}{
		{"whole seconds", liveNow.Add(12 * time.Second), "12s"},
		{"one second", liveNow.Add(time.Second), "1s"},
		{"sub-second remainder rounds up", liveNow.Add(1500 * time.Millisecond), "2s"},
		{"a nanosecond of life is still 1s", liveNow.Add(time.Nanosecond), "1s"},
		{"just under a second", liveNow.Add(999 * time.Millisecond), "1s"},
		{"exact expiry instant clamps", liveNow, "0s"},
		{"past expiry clamps", liveNow.Add(-5 * time.Second), "0s"},
		{"far past clamps", liveNow.Add(-99 * time.Hour), "0s"},
		{"a full TOTP period", liveNow.Add(30 * time.Second), "30s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CountdownText(tt.expiry, liveNow); got != tt.want {
				t.Errorf("CountdownText(%v, %v) = %q, want %q", tt.expiry, liveNow, got, tt.want)
			}
		})
	}
}

func TestLiveSubtitle(t *testing.T) {
	tests := []struct {
		name   string
		sub    string
		expiry time.Time
		want   string
	}{
		{"base plus countdown", "GitHub", liveNow.Add(9 * time.Second), "GitHub · 9s"},
		{"empty base is just the countdown", "", liveNow.Add(9 * time.Second), "9s"},
		{"expired base", "GitHub", liveNow, "GitHub · 0s"},
		{"expired with empty base", "", liveNow.Add(-time.Second), "0s"},
		{"zero expiry leaves the subtitle alone", "GitHub", time.Time{}, "GitHub"},
		{"zero expiry with empty base stays empty", "", time.Time{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LiveSubtitle(tt.sub, tt.expiry, liveNow); got != tt.want {
				t.Errorf("LiveSubtitle(%q, %v, %v) = %q, want %q", tt.sub, tt.expiry, liveNow, got, tt.want)
			}
		})
	}
}

// live builds a result carrying expiry; a zero expiry yields a static row.
func live(id string, expiry time.Time) providers.Result {
	return providers.Result{ID: id, Title: id, Expiry: expiry}
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
