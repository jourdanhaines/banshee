package ui

import (
	"context"
	"testing"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/layershell"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// mockAggregator stands in for the real aggregator (Phase 1, agent E) so the
// UI can be developed and type-checked against the frozen interface alone.
type mockAggregator struct {
	results map[string][]providers.Result
	queries []string
}

func (m *mockAggregator) Query(ctx context.Context, q string) []providers.Result {
	m.queries = append(m.queries, q)
	if err := ctx.Err(); err != nil {
		return nil
	}
	return m.results[q]
}

var _ providers.Aggregator = (*mockAggregator)(nil)

// daemonUI mirrors the interface the daemon (Phase 1, agent D) declares for
// its front-end. *Launcher must satisfy it structurally; this assertion is the
// contract test for that seam.
type daemonUI interface {
	Show(query string)
	Hide()
	Visible() bool
	Reload()
}

var _ daemonUI = (*Launcher)(nil)

func TestMockAggregatorHonorsCancellation(t *testing.T) {
	m := &mockAggregator{results: map[string][]providers.Result{
		"blacksh": {{ID: "1", Title: "blacksheep", Category: providers.CatSession}},
	}}

	if got := m.Query(context.Background(), "blacksh"); len(got) != 1 {
		t.Fatalf("live context: got %d results, want 1", len(got))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := m.Query(ctx, "blacksh"); got != nil {
		t.Errorf("cancelled context: got %d results, want none", len(got))
	}
}

func TestKeyboardModeFor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want layershell.KeyboardMode
	}{
		{"empty defaults to exclusive", "", layershell.KeyboardExclusive},
		{"exclusive", "exclusive", layershell.KeyboardExclusive},
		{"on-demand", "on-demand", layershell.KeyboardOnDemand},
		{"on-demand is case insensitive", "On-Demand", layershell.KeyboardOnDemand},
		{"surrounding whitespace tolerated", "  on-demand  ", layershell.KeyboardOnDemand},
		{"unknown value defaults to exclusive", "sometimes", layershell.KeyboardExclusive},
		{"underscore spelling is not on-demand", "on_demand", layershell.KeyboardExclusive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeyboardModeFor(tt.in); got != tt.want {
				t.Errorf("KeyboardModeFor(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestLauncherFallbacks(t *testing.T) {
	tests := []struct {
		name      string
		cfg       config.Config
		wantWidth int
	}{
		{"zero value config", config.Config{}, config.Default().LauncherWidth},
		{"defaults", config.Default(), 640},
		{"custom", config.Config{MaxResults: 12, LauncherWidth: 900}, 900},
		{"negative values fall back", config.Config{MaxResults: -1, LauncherWidth: -1}, config.Default().LauncherWidth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Launcher{cfg: tt.cfg}
			if got := l.launcherWidth(); got != tt.wantWidth {
				t.Errorf("launcherWidth() = %d, want %d", got, tt.wantWidth)
			}
		})
	}
}

func TestTopMarginFallsBackWithoutDisplay(t *testing.T) {
	// Tests run headless, so GDK has no default display and TopMargin must
	// return the documented fallback rather than panicking.
	if got := TopMargin(); got != fallbackTopMargin {
		t.Errorf("TopMargin() = %d, want %d without a display", got, fallbackTopMargin)
	}
}
