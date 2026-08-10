// Package cliphist is the built-in clipboard-history tool: a wl-paste watcher
// feeds an in-memory ring of past clipboard entries, the provider renders them
// behind the "clip"/"cb" trigger, and the clip-copy/clip-delete handlers
// re-copy or drop an entry.
//
// The history is deliberately session-only. Nothing is written to config or
// data dirs: text lives in daemon memory and image payloads live under the
// user's runtime dir (tmpfs, wiped at logout), so a reboot leaves no trace of
// what was ever copied. Sensitive-looking entries (password-manager hint or
// LooksSecret heuristics) are kept but rendered masked; a future
// clipboard_exclude_hints key could drop hinted captures entirely if keeping
// them proves unwanted.
package cliphist

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind classifies what a history entry holds.
type Kind int

const (
	// KindText is plain text.
	KindText Kind = iota
	// KindImage is an image payload stored as a runtime file.
	KindImage
	// KindFiles is a text/uri-list of copied files.
	KindFiles
)

// Capacity and payload bounds. The 1,000-entry cap is the feature contract;
// the byte bounds exist because entries live in daemon memory for the whole
// session — a single copied video frame must not pin tens of megabytes.
const (
	// DefaultCap is the ring size: the oldest entry is evicted past it.
	DefaultCap = 1000
	// MaxTextBytes drops text captures larger than this outright.
	MaxTextBytes = 1 << 20 // 1 MiB
	// MaxImageBytes drops image captures larger than this outright.
	MaxImageBytes = 10 << 20 // 10 MiB
	// MaxTotalBytes bounds the sum of all retained payloads; exceeding it
	// evicts oldest entries early, keeping ring semantics.
	MaxTotalBytes = 64 << 20 // 64 MiB
	// suppressTTL is how long a SuppressNext hash stays armed. It only needs
	// to cover the wl-paste event round trip; a stale hash must not silently
	// swallow the same text copied again minutes later.
	suppressTTL = 5 * time.Second
)

// Entry is one remembered clipboard capture.
type Entry struct {
	// ID is a store-unique monotonic identifier; actions carry it instead of
	// content.
	ID uint64
	// Kind says how to interpret the payload.
	Kind Kind
	// Text holds the raw payload for KindText and KindFiles (the uri-list
	// verbatim). Empty for KindImage.
	Text string
	// ImagePath is the absolute runtime file holding a KindImage payload.
	ImagePath string
	// MIME is the type the payload was captured as, and the type it is
	// re-offered under on re-copy.
	MIME string
	// Size is the payload length in bytes.
	Size int
	// Hash is the sha256 of the raw payload, used for consecutive dedupe and
	// self-copy suppression.
	Hash [32]byte
	// Copies counts consecutive copies collapsed into this entry (>= 1).
	Copies int
	// Time is when the entry was last copied.
	Time time.Time
	// Sensitive marks an entry whose content must render masked.
	Sensitive bool
	// MaskReason says why ("password manager", "looks like an API key", …);
	// display-only.
	MaskReason string
}

// Store is the in-memory session history. All methods are safe for concurrent
// use: the watcher appends from its own goroutine while the provider reads on
// aggregator goroutines and the handlers mutate on (or detached from) the GTK
// main loop.
type Store struct {
	mu         sync.Mutex
	entries    []Entry // oldest first; List reverses
	capacity   int
	totalBytes int
	nextID     uint64
	imageDir   string
	suppress   map[[32]byte]time.Time
	now        func() time.Time
}

// StoreOption customizes NewStore.
type StoreOption func(*Store)

// WithCap overrides the ring capacity (tests shrink it to observe eviction).
func WithCap(n int) StoreOption {
	return func(s *Store) {
		if n > 0 {
			s.capacity = n
		}
	}
}

// WithImageDir sets the directory image payloads are written to. Empty (the
// default) disables image capture entirely — the store degrades to text-only
// rather than writing payloads to an unintended location.
func WithImageDir(dir string) StoreOption {
	return func(s *Store) { s.imageDir = dir }
}

// WithClock injects the store's clock for tests. (The provider's subtitle
// clock is the separate WithNow Option.)
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// NewStore returns an empty history.
func NewStore(opts ...StoreOption) *Store {
	s := &Store{
		capacity: DefaultCap,
		suppress: make(map[[32]byte]time.Time),
		now:      time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SuppressNext arms a one-shot filter: the next Add whose payload hashes to
// hash (within suppressTTL) is dropped. Boot calls it with the hash of every
// copy banshee itself initiates for secret material, so a TOTP code never
// enters the history — only its hash ever exists outside the copy path.
func (s *Store) SuppressNext(hash [32]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suppress[hash] = s.now().Add(suppressTTL)
}

// Add records a capture. The bool is false when the capture was dropped: an
// armed suppression matched, the payload was over its size bound, or an image
// arrived with no image dir configured.
func (s *Store) Add(kind Kind, mime string, data []byte, sensitive bool, reason string) (Entry, bool) {
	if kind == KindImage {
		if len(data) > MaxImageBytes {
			return Entry{}, false
		}
	} else if len(data) > MaxTextBytes {
		return Entry{}, false
	}

	hash := sha256.Sum256(data)

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	if deadline, ok := s.suppress[hash]; ok {
		delete(s.suppress, hash)
		if now.Before(deadline) {
			return Entry{}, false
		}
	}
	// Expire stale suppressions so an unmatched hash cannot linger forever.
	for h, d := range s.suppress {
		if !now.Before(d) {
			delete(s.suppress, h)
		}
	}

	// Consecutive duplicate: same payload as the newest entry collapses into
	// it — Ctrl+C three times on the same text is one history item.
	if n := len(s.entries); n > 0 && s.entries[n-1].Hash == hash {
		s.entries[n-1].Time = now
		s.entries[n-1].Copies++
		return s.entries[n-1], true
	}

	e := Entry{
		ID:         s.nextID,
		Kind:       kind,
		MIME:       mime,
		Size:       len(data),
		Hash:       hash,
		Copies:     1,
		Time:       now,
		Sensitive:  sensitive,
		MaskReason: reason,
	}
	s.nextID++

	if kind == KindImage {
		if s.imageDir == "" {
			return Entry{}, false
		}
		path := filepath.Join(s.imageDir, fmt.Sprintf("%d%s", e.ID, imageExt(mime)))
		if err := os.MkdirAll(s.imageDir, 0o700); err != nil {
			return Entry{}, false
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return Entry{}, false
		}
		e.ImagePath = path
	} else {
		e.Text = string(data)
	}

	s.entries = append(s.entries, e)
	s.totalBytes += e.Size
	s.evictLocked()
	return e, true
}

// evictLocked drops oldest entries until both the count cap and the byte
// budget hold. Callers hold s.mu.
func (s *Store) evictLocked() {
	drop := 0
	for n := len(s.entries); drop < n-1 &&
		(n-drop > s.capacity || s.totalBytes > MaxTotalBytes); drop++ {
		e := s.entries[drop]
		s.totalBytes -= e.Size
		removeImage(e)
	}
	if drop > 0 {
		s.entries = append(s.entries[:0], s.entries[drop:]...)
	}
}

// List returns every entry, newest first. The slice is a copy; entries share
// no mutable state.
func (s *Store) List() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, e := range s.entries {
		out[len(out)-1-i] = e
	}
	return out
}

// Get returns the entry with the given ID.
func (s *Store) Get(id uint64) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Delete removes the entry with the given ID (and its image file, if any).
func (s *Store) Delete(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.ID == id {
			s.totalBytes -= e.Size
			removeImage(e)
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// Clear empties the history and removes every image file. Called at daemon
// shutdown and when clipboard_history is turned off by a reload.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		removeImage(e)
	}
	s.entries = nil
	s.totalBytes = 0
}

// removeImage best-effort deletes an entry's runtime image file. A failure is
// invisible: the file lives on tmpfs and dies at logout regardless.
func removeImage(e Entry) {
	if e.ImagePath != "" {
		os.Remove(e.ImagePath)
	}
}

// imageExt maps an accepted image MIME to the runtime file extension. Only
// the pixbuf-safe allowlist ever reaches Add (the watcher's classify enforces
// it), so the default is a never-rendered fallback, not a real case.
func imageExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	default:
		return ".img"
	}
}
