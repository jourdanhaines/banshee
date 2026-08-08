package index

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// mkRepo creates dir plus a .git directory inside it.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// tree builds a fixture: root/<a>/... with repos at the given relative paths
// and plain directories at the given plain paths.
func tree(t *testing.T, repos, plain []string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range plain {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range repos {
		mkRepo(t, filepath.Join(root, p))
	}
	return root
}

func names(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func TestScanRootDepthAndSkipDescend(t *testing.T) {
	tests := []struct {
		name     string
		repos    []string
		plain    []string
		maxDepth int
		want     []string
	}{
		{
			name:     "direct children",
			repos:    []string{"alpha", "beta"},
			maxDepth: 5,
			want:     []string{"alpha", "beta"},
		},
		{
			name:     "nested below depth limit",
			repos:    []string{"a/b/c/repo"},
			maxDepth: 5,
			want:     []string{"a/b/c/repo"},
		},
		{
			name:     "beyond depth limit is skipped",
			repos:    []string{"a/b/c/d/e/repo"},
			maxDepth: 3,
			want:     nil,
		},
		{
			name:     "depth counts the .git directory itself",
			repos:    []string{"repo"},
			maxDepth: 2, // repo at depth 1, .git at depth 2 → included
			want:     []string{"repo"},
		},
		{
			name:     "one deeper than the limit is excluded",
			repos:    []string{"repo"},
			maxDepth: 1, // .git would sit at depth 2 → excluded
			want:     nil,
		},
		{
			name:     "submodules inside a repo are not indexed",
			repos:    []string{"outer", "outer/vendor/inner"},
			maxDepth: 6,
			want:     []string{"outer"},
		},
		{
			name:     "root itself can be a repo",
			repos:    []string{"."},
			maxDepth: 5,
			want:     []string{"."},
		},
		{
			name:     "plain directories are ignored",
			repos:    []string{"real"},
			plain:    []string{"empty/nested/deep"},
			maxDepth: 5,
			want:     []string{"real"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := tree(t, tc.repos, tc.plain)
			got := names(root, scanRoot(root, tc.maxDepth))
			want := tc.want
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if len(got) == 0 {
				got = []string{}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("scanRoot = %v, want %v", got, want)
			}
		})
	}
}

func TestScannerCacheRoundTrip(t *testing.T) {
	root := tree(t, []string{"alpha", "beta", "nested/gamma"}, nil)
	cache := filepath.Join(t.TempDir(), "repo_cache")

	s := &Scanner{
		SearchPaths: []string{root},
		MaxDepth:    5,
		TTL:         time.Hour,
		CachePath:   cache,
	}

	repos := s.Repos()
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %v", repos)
	}

	// The cache file is newline separated, sorted and unique.
	b, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if !sort.StringsAreSorted(lines) {
		t.Errorf("cache is not sorted: %v", lines)
	}
	if len(lines) != 3 {
		t.Errorf("cache = %v, want 3 lines", lines)
	}

	// A fresh cache is trusted even when the filesystem changed.
	mkRepo(t, filepath.Join(root, "delta"))
	fresh := &Scanner{SearchPaths: []string{root}, MaxDepth: 5, TTL: time.Hour, CachePath: cache}
	if got := len(fresh.Repos()); got != 3 {
		t.Errorf("fresh cache should be reused, got %d repos", got)
	}

	// An expired cache triggers a rescan.
	stale := &Scanner{SearchPaths: []string{root}, MaxDepth: 5, TTL: time.Nanosecond, CachePath: cache}
	if got := len(stale.Repos()); got != 4 {
		t.Errorf("stale cache should rescan, got %d repos", got)
	}
}

func TestScannerExact(t *testing.T) {
	rootA := tree(t, []string{"alpha", "beta"}, nil)
	rootB := tree(t, []string{"beta"}, nil) // duplicate basename

	s := &Scanner{
		SearchPaths: []string{rootA, rootB},
		MaxDepth:    5,
		TTL:         time.Hour,
		CachePath:   filepath.Join(t.TempDir(), "repo_cache"),
	}

	if repo, ok := s.Exact("alpha"); !ok || repo.Path != filepath.Join(rootA, "alpha") {
		t.Errorf("Exact(alpha) = %+v, %v", repo, ok)
	}
	if _, ok := s.Exact("beta"); ok {
		t.Error("ambiguous basename must not match exactly")
	}
	if _, ok := s.Exact("nope"); ok {
		t.Error("unknown name must not match")
	}
	if _, ok := s.Exact(""); ok {
		t.Error("empty name must not match")
	}
}

func TestScannerClearAndMissingSearchPath(t *testing.T) {
	root := tree(t, []string{"alpha"}, nil)
	cache := filepath.Join(t.TempDir(), "repo_cache")
	s := &Scanner{
		SearchPaths: []string{root, filepath.Join(root, "does-not-exist")},
		MaxDepth:    5,
		TTL:         time.Hour,
		CachePath:   cache,
	}
	if got := len(s.Repos()); got != 1 {
		t.Fatalf("expected 1 repo, got %d", got)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Error("cache file should be gone")
	}
	// Clearing twice is not an error.
	if err := s.Clear(); err != nil {
		t.Errorf("second Clear: %v", err)
	}
}

func TestNames(t *testing.T) {
	root := tree(t, []string{"beta", "alpha"}, nil)
	s := &Scanner{
		SearchPaths: []string{root},
		MaxDepth:    5,
		TTL:         time.Hour,
		CachePath:   filepath.Join(t.TempDir(), "repo_cache"),
	}
	if got := Names(s); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Errorf("Names = %v", got)
	}
}

// TestWriteCacheIsAtomicUnderConcurrency covers the `banshee toggle` spawn
// race: two processes both decide no daemon is running, both spawn one, and
// both refresh the repo index before either reaches the flock. With a shared
// "<cache>.tmp" name their writes interleaved and whichever rename landed last
// published a truncated or spliced repo list as the canonical cache.
func TestWriteCacheIsAtomicUnderConcurrency(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "repo_cache")

	long := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		long = append(long, fmt.Sprintf("/home/dev/repository-with-a-long-name-%03d", i))
	}
	short := []string{"/home/dev/only-one"}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		paths := long
		if i%2 == 1 {
			paths = short
		}
		wg.Add(1)
		go func(paths []string) {
			defer wg.Done()
			s := &Scanner{TTL: time.Hour, CachePath: cache}
			for n := 0; n < 20; n++ {
				if err := s.writeCache(paths); err != nil {
					t.Errorf("writeCache: %v", err)
					return
				}
				// Every read must see one writer's complete list, never a mix.
				got, ok := s.readCache()
				if !ok {
					t.Error("readCache: cache went missing mid-write")
					return
				}
				if len(got) != len(long) && len(got) != len(short) {
					t.Errorf("read %d paths, want %d or %d — the cache was spliced",
						len(got), len(long), len(short))
					return
				}
			}
		}(paths)
	}
	wg.Wait()

	// No temp files left behind.
	entries, err := os.ReadDir(filepath.Dir(cache))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "repo_cache" {
			t.Errorf("leftover file %q in the cache directory", e.Name())
		}
	}
}

// TestConfigureIsRaceFree covers `banshee reload` reconfiguring the scanner on
// the GTK main loop while a background rescan is walking SearchPaths.
func TestConfigureIsRaceFree(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	mkRepo(t, filepath.Join(rootA, "alpha"))
	mkRepo(t, filepath.Join(rootB, "beta"))

	s := &Scanner{
		SearchPaths: []string{rootA},
		MaxDepth:    5,
		TTL:         time.Hour,
		CachePath:   filepath.Join(t.TempDir(), "repo_cache"),
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if i%2 == 0 {
				s.Configure([]string{rootA}, 5, time.Hour)
			} else {
				s.Configure([]string{rootA, rootB}, 3, time.Nanosecond)
			}
		}
	}()
	for i := 0; i < 100; i++ {
		s.Stale()
		_ = s.Rescan()
	}
	<-done
}
