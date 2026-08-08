package connectors

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jourdanhaines/banshee/internal/tmux"
)

// CurrentRepoFunc reports the git repository containing the active pane of
// the most recently active tmux client. ok is false when there is no tmux
// server, no attached client, or the pane's cwd is not inside a git repo.
type CurrentRepoFunc func(ctx context.Context) (root, name string, ok bool)

// currentRepoTTL bounds how often TmuxCurrentRepo re-execs tmux while the
// user is typing: one probe per burst of keystrokes. Negative results are
// cached too, so typing a connector name outside tmux stays cheap.
const currentRepoTTL = 2 * time.Second

// TmuxCurrentRepo returns a CurrentRepoFunc backed by r with a TTL cache.
func TmuxCurrentRepo(r tmux.Runner) CurrentRepoFunc {
	var (
		mu   sync.Mutex
		at   time.Time
		root string
		name string
		ok   bool
	)
	return func(ctx context.Context) (string, string, bool) {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < currentRepoTTL {
			return root, name, ok
		}
		if ctx.Err() != nil {
			return "", "", false
		}
		root, name, ok = probeCurrentRepo(r)
		at = time.Now()
		if ctx.Err() != nil {
			return "", "", false
		}
		return root, name, ok
	}
}

func probeCurrentRepo(r tmux.Runner) (root, name string, ok bool) {
	// tmux expands pane_* formats per client in list-clients (relative to
	// each client's current window's active pane), so one exec yields
	// (activity, cwd) pairs. Fallback if a tmux version misbehaves: pick the
	// client via list-clients, then
	// `display-message -p -t <client_tty> -F '#{pane_current_path}'`.
	out, err := r.Run("list-clients", "-F", "#{client_activity}\t#{pane_current_path}")
	if err != nil {
		return "", "", false
	}
	dir, ok := pickActivePanePath(out)
	if !ok {
		return "", "", false
	}
	gitRoot, ok := FindGitRoot(dir)
	if !ok {
		return "", "", false
	}
	return gitRoot, filepath.Base(gitRoot), true
}

// pickActivePanePath parses
// `list-clients -F '#{client_activity}\t#{pane_current_path}'` output and
// returns the active-pane cwd of the most recently active client. Malformed
// lines and non-absolute paths are skipped.
func pickActivePanePath(out string) (string, bool) {
	var (
		best    string
		bestAct int64 = -1
	)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 || !filepath.IsAbs(fields[1]) {
			continue
		}
		act, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if act > bestAct {
			bestAct = act
			best = fields[1]
		}
	}
	return best, best != ""
}

// FindGitRoot ascends from dir to the filesystem root looking for a
// directory containing .git — a directory or a file, so linked worktrees
// count. ok is false when dir is not inside a git repository.
func FindGitRoot(dir string) (string, bool) {
	dir = filepath.Clean(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
