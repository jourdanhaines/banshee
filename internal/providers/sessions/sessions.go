// Package sessions provides the CatSession rows of the launcher: "Open <name>
// session" for every target banshee can turn into a tmux session.
//
// A "target" is what the CLI accepts as `banshee <target>`: either a git repo
// discovered by internal/index, or a session config in
// ~/.config/banshee/sessions/<target>.json. Both are offered here, exactly as
// banshee_target_pool did in the v0.3 shell implementation.
//
// Activating a row emits providers.ActSession, handled by
// RegisterAttachHandler (attach.go): the session is brought up if needed and
// attached in the most recently active tmux client — whose terminal window is
// raised — falling back to a fresh terminal running `banshee <target>`. The
// AltAction (Tab / Shift+Enter / shift-click) always opens a fresh terminal.
package sessions

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/tmux"
)

// SubtitleSessionConfig is the subtitle used for targets that exist only as a
// session config, with no repo on disk to point at.
const SubtitleSessionConfig = "session config"

// SubtitleRunning is the subtitle used for a running tmux session whose name
// does not resolve back to a known repo.
const SubtitleRunning = "running session"

// Provider lists session targets. Construct it with New.
type Provider struct {
	// index supplies repo targets. May be nil (config targets only).
	index index.Index
	// runner is used only for `tmux ls` on the empty query. May be nil.
	runner tmux.Runner
	// sessionsDir is scanned for *.json session configs.
	sessionsDir string

	// Score ranks a target name against the query. Defaults to fuzzy.Score.
	// Providers that contribute to a repo's result block must all score the
	// repo name with the same Scorer — see providers.ConcurrentAggregator.
	Score fuzzy.Scorer
	// Icon is applied to every emitted result.
	Icon providers.Icon
}

// New returns a session Provider. idx and runner may be nil: a nil index means
// no repo targets, a nil runner means no running-session defaults.
// sessionsDir is normally config.SessionsDir().
func New(idx index.Index, runner tmux.Runner, sessionsDir string) *Provider {
	return &Provider{
		index:       idx,
		runner:      runner,
		sessionsDir: sessionsDir,
		Score:       fuzzy.Score,
		Icon:        providers.Icon{ThemeName: "utilities-terminal-symbolic"},
	}
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "sessions" }

// Query implements providers.Provider.
//
// On a non-empty query it returns every target whose name fuzzy-matches, each
// scored by the target name alone (the shared-score contract). On an empty
// query it returns the currently running tmux sessions instead of the full
// target pool — the launcher's default view is "what is already open", not
// "every repo you own".
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q == "" {
		return p.defaults(ctx)
	}
	return p.matches(ctx, q)
}

// matches scores the union of repo names and session-config basenames.
func (p *Provider) matches(ctx context.Context, q string) ([]providers.Result, error) {
	paths := map[string]string{} // target name → repo path ("" if config only)

	if p.index != nil {
		for _, r := range p.index.Repos() {
			if r.Name == "" {
				continue
			}
			if _, ok := paths[r.Name]; !ok {
				paths[r.Name] = r.Path
			}
		}
	}
	for _, name := range p.configTargets() {
		if _, ok := paths[name]; !ok {
			paths[name] = ""
		}
	}

	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Strings(names)

	score := p.scorer()
	out := make([]providers.Result, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, ok := score(q, name)
		if !ok {
			continue
		}
		subtitle := paths[name]
		if subtitle == "" {
			subtitle = SubtitleSessionConfig
		}
		out = append(out, p.result(name, subtitle, s))
	}
	return out, nil
}

// defaults returns the running tmux sessions (empty query view).
func (p *Provider) defaults(ctx context.Context) ([]providers.Result, error) {
	if p.runner == nil {
		return nil, nil
	}
	out, err := p.runner.Run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		// No server running is the common case and is not an error worth
		// surfacing in the launcher.
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var res []providers.Result
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true

		subtitle := SubtitleRunning
		if p.index != nil {
			if repo, ok := p.index.Exact(name); ok {
				subtitle = repo.Path
			}
		}
		res = append(res, p.result(name, subtitle, 0))
	}
	return res, nil
}

// result builds one CatSession row for target: attach in the last active
// terminal by default, always-a-new-terminal as the alternate action.
func (p *Provider) result(target, subtitle string, score int) providers.Result {
	return providers.Result{
		ID:       "sessions:" + target,
		Title:    "Open " + target + " session",
		Subtitle: subtitle,
		Icon:     p.Icon,
		Category: providers.CatSession,
		Score:    score,
		Action: providers.Action{
			Kind:   providers.ActSession,
			Target: target,
		},
		AltAction: &providers.Action{
			Kind:     providers.ActSession,
			Target:   target,
			ForceNew: true,
		},
	}
}

// configTargets returns the basenames of sessionsDir/*.json.
func (p *Provider) configTargets() []string {
	if p.sessionsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(p.sessionsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".json"))
	}
	return names
}

func (p *Provider) scorer() fuzzy.Scorer {
	if p.Score != nil {
		return p.Score
	}
	return fuzzy.Score
}

// SelfBinary resolves the running banshee executable, falling back to the
// bare name so a PATH lookup still works when /proc is unavailable. Boot uses
// it to build the `<terminal> -e banshee <target>` fallback argv.
func SelfBinary() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return "banshee"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		return resolved
	}
	return exe
}
