package boot

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/plugins"
)

func TestPluginSetNoPlugins(t *testing.T) {
	// An empty plugin dir must yield no results and no error, not a failure
	// that would drop the whole plugin category.
	host := plugins.NewHost(filepath.Join(t.TempDir(), "plugins"), plugins.Options{})
	if err := host.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer host.Shutdown()

	ps := &pluginSet{host: host}
	if ps.Name() != "plugins" {
		t.Errorf("Name = %q", ps.Name())
	}
	got, err := ps.Query(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d results, want 0", len(got))
	}
}

func TestPluginSetCancelled(t *testing.T) {
	host := plugins.NewHost(filepath.Join(t.TempDir(), "plugins"), plugins.Options{})
	ps := &pluginSet{host: host}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ps.Query(ctx, "x"); err == nil {
		t.Fatal("want ctx error, got nil")
	}
}

// stubUI records the calls the daemon would make.
type stubUI struct {
	mu     sync.Mutex
	shows  []string
	hides  int
	reload int
	vis    bool
}

func (s *stubUI) Show(q string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shows = append(s.shows, q)
}
func (s *stubUI) Hide() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hides++
}
func (s *stubUI) Visible() bool { return s.vis }
func (s *stubUI) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reload++
}

func TestReindexOnShow(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repos", "alpha")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		warmCache  bool
		backdate   bool
		wantRescan bool
	}{
		{name: "stale cache triggers a rescan", warmCache: true, backdate: true, wantRescan: true},
		{name: "missing cache triggers a rescan", wantRescan: true},
		{name: "fresh cache does not rescan", warmCache: true, wantRescan: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "repo_cache")
			idx := &index.Scanner{
				SearchPaths: []string{filepath.Join(dir, "repos")},
				MaxDepth:    5,
				TTL:         time.Hour,
				CachePath:   cache,
			}
			if tt.warmCache {
				if err := idx.Rescan(); err != nil {
					t.Fatal(err)
				}
			}
			if tt.backdate {
				old := time.Now().Add(-2 * time.Hour)
				if err := os.Chtimes(cache, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if got := idx.Stale(); got != tt.wantRescan {
				t.Fatalf("Stale() = %v, want %v", got, tt.wantRescan)
			}

			ui := &stubUI{}
			r := &reindexOnShow{UI: ui, idx: idx}
			r.Show("query")
			r.Hide()
			r.Reload()

			// Let any background rescan finish.
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) && idx.Stale() {
				time.Sleep(5 * time.Millisecond)
			}

			ui.mu.Lock()
			defer ui.mu.Unlock()
			if len(ui.shows) != 1 || ui.shows[0] != "query" {
				t.Errorf("shows = %v, want [query]", ui.shows)
			}
			if ui.hides != 1 || ui.reload != 1 {
				t.Errorf("hides=%d reload=%d, want 1/1", ui.hides, ui.reload)
			}
			if idx.Stale() {
				t.Error("index still stale after show")
			}
			if len(idx.Repos()) != 1 {
				t.Errorf("repos = %v, want 1", idx.Repos())
			}
		})
	}
}

func TestReindexOnShowNilIndex(t *testing.T) {
	ui := &stubUI{}
	r := &reindexOnShow{UI: ui}
	r.Show("") // must not panic
	if len(ui.shows) != 1 {
		t.Fatalf("shows = %v", ui.shows)
	}
}

var _ providers.Provider = (*pluginSet)(nil)
