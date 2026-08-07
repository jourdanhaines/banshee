// Package index discovers git repositories under the configured search paths
// and caches the result (same cache file and TTL semantics as banshee v0.3).
//
// index.go (the Index interface and Repo type) is a frozen Phase-0 contract;
// the scanner lands in Phase 1.
package index

// Repo is one discovered repository.
type Repo struct {
	Name string // basename
	Path string // absolute path
}

// Index is the read surface consumed by providers. Implementations must be
// safe for concurrent use.
type Index interface {
	// Repos returns all known repos (cached or freshly scanned).
	Repos() []Repo
	// Exact returns the repo whose basename equals name; ok is false when
	// zero or multiple repos match (ambiguity falls through to the picker).
	Exact(name string) (Repo, bool)
	// Refresh rescans if the cache TTL has expired.
	Refresh() error
	// Clear removes the on-disk cache.
	Clear() error
}
