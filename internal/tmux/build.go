package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/session"
)

// Builder turns a session config into tmux commands. Every tmux invocation
// goes through R, so tests assert exact argv without a server.
//
// The zero value is not usable; use NewBuilder.
type Builder struct {
	// R runs tmux. Required.
	R Runner

	// Home is the last-resort working directory (empty → $HOME).
	Home string
	// DirExists reports whether a path is an existing directory. nil uses
	// the filesystem; tests override it to keep fixtures hermetic.
	DirExists func(path string) bool
	// InTmux reports whether banshee runs inside a tmux client. nil checks
	// $TMUX.
	InTmux func() bool
	// AvailableFunc overrides the tmux-on-PATH check. nil uses Available.
	AvailableFunc func() bool
	// AttachFunc replaces the attach step when running outside tmux. The
	// CLI sets it to ExecAttach so tmux inherits the terminal's TTY;
	// programmatic callers leave it nil and attach through R.
	AttachFunc func(name string) error
	// Log receives progress and non-fatal messages (stderr in the CLI).
	Log func(format string, args ...any)
}

// compile-time check: Builder is the session package's tmux seam.
var _ session.Builder = (*Builder)(nil)

// NewBuilder returns a Builder driving r.
func NewBuilder(r Runner) *Builder { return &Builder{R: r} }

// Available reports whether tmux can be used.
func (b *Builder) Available() bool {
	if b.AvailableFunc != nil {
		return b.AvailableFunc()
	}
	return Available()
}

// SessionName derives the tmux session name from a target name.
func (b *Builder) SessionName(target string) string { return SessionName(target) }

// HasSession reports whether a session with this exact name exists.
func (b *Builder) HasSession(name string) bool {
	_, err := b.R.Run("has-session", "-t", "="+name)
	return err == nil
}

// CreatePlainSession creates a bare detached session at cwd. It is idempotent:
// an existing session of the same name is left alone.
func (b *Builder) CreatePlainSession(name, cwd string) error {
	if b.HasSession(name) {
		return nil
	}
	_, err := b.R.Run("new-session", "-d", "-s", name, "-c", cwd)
	return Detail(err)
}

// AttachOrSwitch attaches to a session, or switches the current client to it
// when banshee already runs inside tmux.
func (b *Builder) AttachOrSwitch(name string) error {
	if b.inTmux() {
		// Parity with v0.3: a failed switch-client is not fatal.
		_, _ = b.R.Run("switch-client", "-t", "="+name)
		return nil
	}
	if b.AttachFunc != nil {
		return b.AttachFunc(name)
	}
	_, err := b.R.Run("attach-session", "-t", "="+name)
	return Detail(err)
}

// BuildSession creates the windows and panes described by s under the tmux
// session named after target. It is idempotent: if the session already exists
// nothing is changed.
//
// defaultCwd is the working directory used when the config names none —
// the matching repository path, or the user's home directory.
func (b *Builder) BuildSession(target string, s session.Session, defaultCwd string) error {
	name := SessionName(target)
	if b.HasSession(name) {
		b.logf("session %q already running — skipping", name)
		return nil
	}

	scwd := s.Cwd
	if scwd == "" {
		scwd = defaultCwd
	}
	scwd = config.ExpandPath(scwd)
	if !b.dirExists(scwd) {
		scwd = b.home()
	}

	if len(s.Windows) == 0 {
		return errors.New("session config has no windows")
	}

	for i, w := range s.Windows {
		wcwd := w.Cwd
		if wcwd == "" {
			wcwd = scwd
		}
		wcwd = config.ExpandPath(wcwd)
		if !b.dirExists(wcwd) {
			wcwd = scwd
		}

		var args []string
		if i == 0 {
			args = []string{"new-session", "-d", "-s", name}
		} else {
			args = []string{"new-window", "-d", "-t", "=" + name + ":"}
		}
		if w.Name != "" {
			args = append(args, "-n", w.Name)
		}
		args = append(args, "-c", wcwd)
		if _, err := b.R.Run(args...); err != nil {
			return fmt.Errorf("tmux %s failed: %w", args[0], Detail(err))
		}

		first, err := b.R.Run("display-message", "-p", "-t", "="+name+":{end}", "#{pane_id}")
		if err != nil || first == "" {
			return fmt.Errorf("failed to resolve first pane id for %s", name)
		}

		if err := b.buildPanes(first, w.Panes, wcwd, 0); err != nil {
			return err
		}
	}

	// Selecting the first window is cosmetic; failure is not fatal.
	_, _ = b.R.Run("select-window", "-t", "="+name+":^")
	b.logf("built session %q", name)
	return nil
}

// buildPanes is the two-phase recursive pane walker, an exact port of
// banshee_build_panes: first every sibling split is created (alternating
// columns at even depths and rows at odd depths, each taking a shrinking
// percentage of the remaining space), then each resulting pane is populated —
// nested arrays recurse one depth deeper, leaves get their cwd and command
// typed in.
func (b *Builder) buildPanes(targetPane string, panes []session.Pane, baseCwd string, depth int) error {
	n := len(panes)
	if n == 0 {
		return nil
	}

	dir := "-v"
	if depth%2 == 0 {
		dir = "-h"
	}

	ids := make([]string, 0, n)
	ids = append(ids, targetPane)
	prev := targetPane
	for i := 1; i < n; i++ {
		pct := 100 - 100/(n-i+1)
		if pct < 1 {
			pct = 1
		}
		if pct > 99 {
			pct = 99
		}
		id, err := b.R.Run("split-window", "-P", "-F", "#{pane_id}", dir,
			"-l", fmt.Sprintf("%d%%", pct), "-t", prev, "-c", baseCwd)
		if err != nil {
			return fmt.Errorf("split-window failed (depth=%d, i=%d): %w", depth, i, Detail(err))
		}
		ids = append(ids, id)
		prev = id
	}

	for i, id := range ids {
		if id == "" {
			continue
		}
		p := panes[i]
		if p.IsSplit() {
			sub, err := p.Split()
			if err != nil {
				return fmt.Errorf("invalid nested panes at depth %d index %d: %w", depth, i, err)
			}
			if err := b.buildPanes(id, sub, baseCwd, depth+1); err != nil {
				return err
			}
			continue
		}
		leaf, err := p.Leaf()
		if err != nil {
			return fmt.Errorf("invalid pane at depth %d index %d: %w", depth, i, err)
		}
		if leaf.Cwd != "" {
			cwd := config.ExpandPath(leaf.Cwd)
			if b.dirExists(cwd) {
				if err := b.sendLine(id, "cd "+cwd); err != nil {
					return err
				}
			}
		}
		if leaf.Run != "" {
			if err := b.sendLine(id, leaf.Run); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendLine types a literal line into a pane and presses Enter. The two calls
// are deliberately separate: -l keeps the text literal, so Enter cannot be
// part of it.
func (b *Builder) sendLine(pane, text string) error {
	if _, err := b.R.Run("send-keys", "-t", pane, "-l", text); err != nil {
		return fmt.Errorf("send-keys failed for pane %s: %w", pane, Detail(err))
	}
	if _, err := b.R.Run("send-keys", "-t", pane, "Enter"); err != nil {
		return fmt.Errorf("send-keys Enter failed for pane %s: %w", pane, Detail(err))
	}
	return nil
}

func (b *Builder) dirExists(p string) bool {
	if b.DirExists != nil {
		return b.DirExists(p)
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (b *Builder) inTmux() bool {
	if b.InTmux != nil {
		return b.InTmux()
	}
	return os.Getenv("TMUX") != ""
}

func (b *Builder) home() string {
	if b.Home != "" {
		return b.Home
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return h
}

func (b *Builder) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log(format, args...)
	}
}

// ExecAttach hands the current process over to tmux so the client inherits
// the terminal's TTY. It only returns on failure. Inside tmux it switches the
// existing client instead of nesting.
func ExecAttach(name string) error {
	path, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux is not installed: %w", err)
	}
	argv := []string{"tmux", "attach-session", "-t", "=" + name}
	if os.Getenv("TMUX") != "" {
		argv = []string{"tmux", "switch-client", "-t", "=" + name}
	}
	return syscall.Exec(path, argv, os.Environ())
}
