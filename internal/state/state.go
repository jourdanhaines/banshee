// Package state persists banshee's small pieces of cross-invocation state.
// Today that is the last action (last_action), written in the same
// "<kind>:<name>" one-line format as banshee v0.3 so an upgrade keeps the
// user's `banshee -r` working.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jourdanhaines/banshee/internal/config"
)

// Action kinds stored in last_action.
const (
	KindTarget = "target"
	KindGroup  = "group"
)

// ErrNoAction reports that no last action has been recorded yet.
var ErrNoAction = errors.New("no previous action")

// Action is a parsed last_action entry.
type Action struct {
	Kind string // KindTarget or KindGroup
	Name string
}

// String renders the on-disk form, without the trailing newline.
func (a Action) String() string { return a.Kind + ":" + a.Name }

// Store reads and writes the last_action file.
type Store struct {
	// Path is the last_action file. Empty means config.LastActionPath().
	Path string
}

// Default returns a Store pointing at the standard last_action path.
func Default() *Store { return &Store{Path: config.LastActionPath()} }

func (s *Store) path() string {
	if s.Path != "" {
		return s.Path
	}
	return config.LastActionPath()
}

// Record atomically stores the last action. Empty kind or name is a no-op, so
// callers need not guard.
func (s *Store) Record(kind, name string) error {
	if kind == "" || name == "" {
		return nil
	}
	path := s.path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(kind+":"+name+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Read returns the recorded action. ErrNoAction is returned when the file is
// missing or empty; a malformed entry yields a descriptive error.
func (s *Store) Read() (Action, error) {
	b, err := os.ReadFile(s.path())
	if err != nil {
		return Action{}, ErrNoAction
	}
	line := b2line(b)
	if line == "" {
		return Action{}, ErrNoAction
	}
	kind, name, ok := strings.Cut(line, ":")
	if !ok || kind == "" || name == "" {
		return Action{}, fmt.Errorf("malformed last_action: %s", line)
	}
	return Action{Kind: kind, Name: name}, nil
}

// Restore reads the last action and dispatches it: onTarget for a target,
// onGroup for a group. Keeping the callbacks here avoids a state→session
// dependency, so either side stays swappable.
func (s *Store) Restore(onTarget, onGroup func(name string) error) error {
	a, err := s.Read()
	if err != nil {
		return err
	}
	switch a.Kind {
	case KindTarget:
		if onTarget == nil {
			return fmt.Errorf("no handler for last action kind %q", a.Kind)
		}
		return onTarget(a.Name)
	case KindGroup:
		if onGroup == nil {
			return fmt.Errorf("no handler for last action kind %q", a.Kind)
		}
		return onGroup(a.Name)
	default:
		return fmt.Errorf("unknown last_action type %q", a.Kind)
	}
}

// Migrate cleans up state left by older banshee versions in dataDir: the
// pre-0.2 flat "sessions" and "session_state" files are removed, and a 0.2.0
// "last_loaded" bundle name becomes a "target:" last action. It is a no-op
// once migrated and never fails an invocation.
func (s *Store) Migrate(dataDir string) {
	for _, stale := range []string{"sessions", "session_state"} {
		_ = os.Remove(filepath.Join(dataDir, stale))
	}

	old := filepath.Join(dataDir, "last_loaded")
	b, err := os.ReadFile(old)
	if err != nil {
		return
	}
	if _, err := os.Stat(s.path()); err != nil {
		if name := b2line(b); name != "" {
			_ = s.Record(KindTarget, name)
		}
	}
	_ = os.Remove(old)
}

// b2line returns the first line of b with CR/LF stripped.
func b2line(b []byte) string {
	s := string(b)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, "\r")
}
