package session

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/jourdanhaines/banshee/internal/index"
)

// Mode selects what Resolve does when a target has no session config.
type Mode int

const (
	// ModeDefault falls back to a plain session at the matching repo, and
	// fails when there is no repo either.
	ModeDefault Mode = iota
	// ModeRequireConfig drops into the editor flow to create a config
	// (banshee -s).
	ModeRequireConfig
)

// Builder is the tmux side of resolution. internal/tmux.Builder implements
// it; keeping it an interface here avoids a session→tmux import cycle and
// lets tests resolve targets without a tmux server.
type Builder interface {
	// Available reports whether tmux can be used at all.
	Available() bool
	// SessionName derives the tmux session name from a target name.
	SessionName(target string) string
	// HasSession reports whether a session with that exact name is running.
	HasSession(name string) bool
	// BuildSession creates the windows and panes described by s. It must be
	// idempotent: an already-running session is left untouched.
	BuildSession(target string, s Session, defaultCwd string) error
	// CreatePlainSession creates a bare detached session at cwd.
	CreatePlainSession(name, cwd string) error
	// AttachOrSwitch attaches to (or switches the client to) a session.
	AttachOrSwitch(name string) error
}

// Recorder persists the last action so banshee -r can replay it.
// internal/state.Store implements it.
type Recorder interface {
	Record(kind, name string) error
}

// ErrGroupMissing is returned by ResolveGroup when groups/<name>.json does
// not exist; the CLI answers it with the multi-select creation prompt.
var ErrGroupMissing = errors.New("group config does not exist")

// Resolver ties session configs, the repo index and the tmux builder
// together. It is the single entry point every front-end (CLI, launcher,
// group loader) uses to bring a target up.
type Resolver struct {
	// SessionsDir holds sessions/<target>.json files.
	SessionsDir string
	// GroupsDir holds groups/<name>.json files.
	GroupsDir string
	// Index resolves a target name to a repository path. May be nil.
	Index index.Index
	// Builder performs the tmux work. Required.
	Builder Builder
	// Recorder records the last action. May be nil.
	Recorder Recorder
	// EditSession is invoked by ModeRequireConfig when no config exists. It
	// must create/edit the config and then load it (the CLI wires this to
	// its editor loop). May be nil, in which case resolution fails.
	EditSession func(target string) error
	// Home is the fallback cwd when nothing else is known. Empty means
	// $HOME as reported by the OS.
	Home string
	// Log receives non-fatal progress messages (stderr in the CLI). May be
	// nil.
	Log func(format string, args ...any)
}

// Resolve brings up one target: build its configured session, or fall back to
// a plain session at the matching repo, then record it and optionally attach.
func (r *Resolver) Resolve(target string, mode Mode, attach bool) error {
	if target == "" {
		return errors.New("empty target")
	}
	if r.Builder == nil {
		return errors.New("no tmux builder configured")
	}
	if !r.Builder.Available() {
		return errors.New("tmux is not installed")
	}

	cfg := SessionPath(r.SessionsDir, target)
	repoPath := r.repoPath(target)

	switch {
	case fileExists(cfg):
		s, err := LoadSession(cfg)
		if err != nil {
			return err
		}
		defaultCwd := repoPath
		if defaultCwd == "" {
			defaultCwd = r.home()
		}
		if err := r.Builder.BuildSession(target, s, defaultCwd); err != nil {
			return err
		}
	case mode == ModeRequireConfig:
		if r.EditSession == nil {
			return fmt.Errorf("no config for %q and no editor flow available", target)
		}
		// The editor flow loads the target itself once the config is valid.
		return r.EditSession(target)
	case repoPath == "":
		// No config and no repo — but a session by that name may already be
		// running (e.g. a bare `tmux new` session picked from the launcher).
		// Attaching to it is the only sensible meaning of the target then.
		if !r.Builder.HasSession(r.Builder.SessionName(target)) {
			return fmt.Errorf("no config or matching repo for %q", target)
		}
	default:
		if err := r.Builder.CreatePlainSession(r.Builder.SessionName(target), repoPath); err != nil {
			return err
		}
	}

	r.record("target", target)

	if attach {
		return r.Builder.AttachOrSwitch(r.Builder.SessionName(target))
	}
	return nil
}

// ResolveGroup loads every target of a group (logging, not aborting, on
// per-target failures) and attaches to the first one. ErrGroupMissing is
// returned when the group config does not exist.
func (r *Resolver) ResolveGroup(name string, attach bool) error {
	if name == "" {
		return errors.New("empty group name")
	}
	if r.Builder == nil {
		return errors.New("no tmux builder configured")
	}
	if !r.Builder.Available() {
		return errors.New("tmux is not installed")
	}

	cfg := GroupPath(r.GroupsDir, name)
	if !fileExists(cfg) {
		return ErrGroupMissing
	}
	g, err := LoadGroup(cfg)
	if err != nil {
		return err
	}
	if len(g.Targets) == 0 {
		return fmt.Errorf("group %q has no targets", name)
	}

	first := ""
	for _, t := range g.Targets {
		if t == "" {
			continue
		}
		if first == "" {
			first = t
		}
		if err := r.Resolve(t, ModeDefault, false); err != nil {
			r.logf("target %q failed to load: %v", t, err)
		}
	}

	r.record("group", name)

	if first == "" || !attach {
		return nil
	}
	return r.Builder.AttachOrSwitch(r.Builder.SessionName(first))
}

// Targets returns the target names that have a session config.
func (r *Resolver) Targets() []string { return ListTargets(r.SessionsDir) }

// Groups returns the group names that have a group config.
func (r *Resolver) Groups() []string { return ListGroups(r.GroupsDir) }

// Pool is the union of repo basenames and configured target names, sorted and
// deduplicated — the selectable set when composing a group.
func (r *Resolver) Pool() []string {
	seen := map[string]struct{}{}
	if r.Index != nil {
		for _, name := range index.Names(r.Index) {
			seen[name] = struct{}{}
		}
	}
	for _, t := range ListTargets(r.SessionsDir) {
		seen[t] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (r *Resolver) repoPath(target string) string {
	if r.Index == nil {
		return ""
	}
	if repo, ok := r.Index.Exact(target); ok {
		return repo.Path
	}
	return ""
}

func (r *Resolver) record(kind, name string) {
	if r.Recorder == nil {
		return
	}
	if err := r.Recorder.Record(kind, name); err != nil {
		r.logf("could not record last action: %v", err)
	}
}

func (r *Resolver) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

func (r *Resolver) home() string {
	if r.Home != "" {
		return r.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
