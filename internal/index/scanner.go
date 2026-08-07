package index

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
)

// Scanner is the filesystem-backed Index. It walks the configured search
// paths for git repositories and caches the result in a newline-separated
// file (the same repo_cache format and TTL semantics as banshee v0.3), so the
// shell CLI and the daemon share one cache.
//
// Scanner is safe for concurrent use. SearchPaths, MaxDepth and TTL may be
// assigned directly while the Scanner is still private to one goroutine
// (construction, tests); once it is shared — the daemon rescans in the
// background while `banshee reload` re-reads the config on the GTK main loop —
// change them through Configure, which takes the same lock the readers do.
type Scanner struct {
	// SearchPaths are scanned in order; a leading ~ is expanded.
	SearchPaths []string
	// MaxDepth caps how deep below a search path a .git directory may sit
	// (matching `find -maxdepth N -type d -name .git`).
	MaxDepth int
	// TTL is how long the cache file stays fresh.
	TTL time.Duration
	// CachePath is the repo_cache location. It is fixed for the lifetime of
	// the Scanner (it is an XDG path, not a config key).
	CachePath string

	mu     sync.RWMutex
	repos  []Repo
	loaded bool
}

// compile-time check.
var _ Index = (*Scanner)(nil)

// NewScanner builds a Scanner from a parsed config.
func NewScanner(cfg config.Config) *Scanner {
	return &Scanner{
		SearchPaths: cfg.SearchPaths,
		MaxDepth:    cfg.MaxDepth,
		TTL:         time.Duration(cfg.CacheTTL) * time.Second,
		CachePath:   config.RepoCachePath(),
	}
}

// Configure replaces the scan settings that banshee.conf owns. It is the
// reload path: a plain assignment to the fields would be a torn write against
// a rescan already walking SearchPaths.
func (s *Scanner) Configure(searchPaths []string, maxDepth int, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SearchPaths = append([]string(nil), searchPaths...)
	s.MaxDepth = maxDepth
	s.TTL = ttl
}

// settings snapshots the reconfigurable fields under the lock.
func (s *Scanner) settings() (searchPaths []string, maxDepth int, ttl time.Duration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.SearchPaths...), s.MaxDepth, s.TTL
}

// Repos returns all known repos, loading the cache or scanning on first use.
// The returned slice is a copy: callers may sort or filter it freely.
func (s *Scanner) Repos() []Repo {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()

	if !loaded {
		// Best effort: a failed refresh still returns whatever we have.
		_ = s.Refresh()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Repo(nil), s.repos...)
}

// Exact returns the repo whose basename equals name. ok is false when zero or
// more than one repo matches, so ambiguity falls through to the picker.
func (s *Scanner) Exact(name string) (Repo, bool) {
	if name == "" {
		return Repo{}, false
	}
	var match Repo
	n := 0
	for _, r := range s.Repos() {
		if r.Name == name {
			match = r
			n++
		}
	}
	return match, n == 1
}

// Refresh loads the cache when it is younger than TTL and rescans otherwise,
// rewriting the cache file.
func (s *Scanner) Refresh() error {
	if paths, ok := s.readCache(); ok {
		s.set(paths)
		return nil
	}
	paths := s.scan()
	s.set(paths)
	return s.writeCache(paths)
}

// Rescan forces a filesystem scan, ignoring cache freshness.
func (s *Scanner) Rescan() error {
	paths := s.scan()
	s.set(paths)
	return s.writeCache(paths)
}

// Clear removes the on-disk cache and forgets the in-memory repo list.
func (s *Scanner) Clear() error {
	s.mu.Lock()
	s.repos = nil
	s.loaded = false
	s.mu.Unlock()
	if err := os.Remove(s.CachePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Stale reports whether the cache file is missing or older than TTL.
func (s *Scanner) Stale() bool {
	s.mu.RLock()
	ttl := s.TTL
	s.mu.RUnlock()
	fi, err := os.Stat(s.CachePath)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) >= ttl
}

func (s *Scanner) set(paths []string) {
	repos := make([]Repo, 0, len(paths))
	for _, p := range paths {
		repos = append(repos, Repo{Name: filepath.Base(p), Path: p})
	}
	s.mu.Lock()
	s.repos = repos
	s.loaded = true
	s.mu.Unlock()
}

func (s *Scanner) readCache() ([]string, bool) {
	if s.CachePath == "" || s.Stale() {
		return nil, false
	}
	b, err := os.ReadFile(s.CachePath)
	if err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, true
}

func (s *Scanner) writeCache(paths []string) error {
	if s.CachePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.CachePath), 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	for _, p := range paths {
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	// A per-process temp name, not "<cache>.tmp": two banshee processes can
	// legitimately rewrite the cache at once (the toggle spawn race has both
	// candidates refreshing the index before either reaches the flock), and a
	// shared temp path would let them interleave into a spliced repo list.
	tmp, err := os.CreateTemp(filepath.Dir(s.CachePath), filepath.Base(s.CachePath)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if _, err := tmp.WriteString(sb.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.CachePath)
}

// scan walks every search path and returns sorted, unique repository paths.
func (s *Scanner) scan() []string {
	searchPaths, maxDepth, _ := s.settings()
	seen := map[string]struct{}{}
	for _, raw := range searchPaths {
		root := config.ExpandPath(raw)
		if root == "" {
			continue
		}
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		for _, p := range scanRoot(root, maxDepth) {
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// scanRoot walks one search path. A directory containing a .git directory is
// recorded as a repository and not descended into (nested submodules and
// vendored checkouts stay out of the index). maxDepth counts the .git
// directory itself, matching the legacy `find -maxdepth` semantics.
func scanRoot(root string, maxDepth int) []string {
	var found []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable directory: skip it, keep walking (find 2>/dev/null).
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if depth(root, path)+1 > maxDepth {
			return fs.SkipDir
		}
		if isDir(filepath.Join(path, ".git")) {
			found = append(found, path)
			return fs.SkipDir
		}
		return nil
	})
	return found
}

// depth returns how many path elements separate path from root.
func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Names returns the sorted, unique basenames of all known repos. It is the
// completion and picker surface.
func Names(idx Index) []string {
	seen := map[string]struct{}{}
	for _, r := range idx.Repos() {
		seen[r.Name] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
