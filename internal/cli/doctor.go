package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/ipc"
	"github.com/jourdanhaines/banshee/internal/launch"
)

// HyprlandSnippet is the Hyprland configuration banshee needs: blur for the
// layer surface and the launcher bound to $menu.
const HyprlandSnippet = `layerrule {
    name = banshee
    match:namespace = banshee
    blur = on
    ignore_alpha = 0
}
$menu = banshee toggle
`

// doctor prints an installation diagnosis: required tools, terminal
// resolution, repo index health and daemon liveness, followed by the Hyprland
// snippet. It fails (exit 1) when a required check fails.
func (a *App) doctor() error {
	failures := 0

	check := func(ok bool, required bool, name, detail string) {
		switch {
		case ok:
			fmt.Fprintf(a.Out, "  [ ok ] %-14s %s\n", name, detail)
		case required:
			failures++
			fmt.Fprintf(a.Out, "  [fail] %-14s %s\n", name, detail)
		default:
			fmt.Fprintf(a.Out, "  [warn] %-14s %s\n", name, detail)
		}
	}

	fmt.Fprintf(a.Out, "banshee %s\n\nTools:\n", config.Version)

	// tmux is a warning, not a failure: without it `banshee [query]` still
	// works as a repo jumper through the shell plugins' cd fallback, which is
	// what README calls "Recommended".
	if path, err := exec.LookPath("tmux"); err == nil {
		check(true, false, "tmux", path)
	} else {
		check(false, false, "tmux",
			"not found — session features are unavailable; `banshee [query]` cd's instead (shell plugin required)")
	}
	if path, err := exec.LookPath("fzf"); err == nil {
		check(true, false, "fzf", path)
	} else {
		check(false, false, "fzf", "not found — the CLI falls back to a numbered picker")
	}
	if path, err := exec.LookPath("git"); err == nil {
		check(true, false, "git", path)
	} else {
		check(false, false, "git", "not found")
	}

	term, termErr := launch.ResolveTerminal(launch.Options{Terminal: a.Cfg.Terminal})
	if termErr == nil {
		check(true, true, "terminal", term)
	} else {
		check(false, true, "terminal", termErr.Error())
	}

	fmt.Fprintf(a.Out, "\nConfig:\n")
	confDetail := config.ConfPath()
	if !fileExists(confDetail) {
		confDetail += " (missing — using defaults)"
	}
	check(true, false, "conf", confDetail)
	check(true, false, "search_paths", strings.Join(a.Cfg.SearchPaths, ", "))
	check(true, false, "sessions", config.SessionsDir())
	check(true, false, "groups", config.GroupsDir())

	fmt.Fprintf(a.Out, "\nIndex:\n")
	repos := a.Index.Repos()
	detail := fmt.Sprintf("%d repositories, cache %s", len(repos), config.RepoCachePath())
	check(len(repos) > 0, false, "repos", detail)
	if len(repos) > 0 {
		check(true, false, "sample", strings.Join(firstN(index.Names(a.Index), 5), ", "))
	}

	if a.Hooks.Plugins != nil {
		fmt.Fprintf(a.Out, "\nPlugins (%s):\n", config.PluginsDir())
		names, loadErr := a.Hooks.Plugins()
		if len(names) == 0 {
			check(true, false, "loaded", "none")
		}
		for _, name := range names {
			check(true, false, "loaded", name)
		}
		if loadErr != nil {
			// A plugin that fails to load is skipped, not fatal: the rest of
			// the launcher works, so this is a warning.
			check(false, false, "errors", loadErr.Error())
		}
	}

	fmt.Fprintf(a.Out, "\nDaemon:\n")
	sock, err := ipc.SocketPath()
	switch {
	case err != nil:
		check(false, false, "socket", err.Error())
	default:
		resp, pingErr := ipc.Ping()
		if pingErr != nil {
			check(false, false, "socket", fmt.Sprintf("%s — not running (%v)", sock, pingErr))
		} else {
			check(true, false, "socket", fmt.Sprintf("%s — running, version %s", sock, resp.Version))
		}
	}

	fmt.Fprintf(a.Out, "\nHyprland (~/.config/hypr/hyprland.conf):\n\n")
	for _, line := range strings.Split(strings.TrimRight(HyprlandSnippet, "\n"), "\n") {
		fmt.Fprintf(a.Out, "  %s\n", line)
	}
	fmt.Fprintln(a.Out)

	if failures > 0 {
		return fmt.Errorf("%d check(s) failed", failures)
	}
	return nil
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
