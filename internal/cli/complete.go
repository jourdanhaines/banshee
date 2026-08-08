package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/session"
	"github.com/jourdanhaines/banshee/internal/state"
)

// complete implements the hidden `banshee _complete <kind>` subcommand used by
// the shell plugins. It prints one candidate per line so completion needs no
// JSON parsing or shell-side caching.
func (a *App) complete(args []string) error {
	kind := ""
	if len(args) > 0 {
		kind = args[0]
	}
	var out []string
	switch kind {
	case "repos":
		out = index.Names(a.Index)
	case "targets":
		out = session.ListTargets(a.Res.SessionsDir)
	case "groups":
		out = session.ListGroups(a.Res.GroupsDir)
	case "pool":
		out = a.Res.Pool()
	default:
		return fmt.Errorf("_complete: unknown kind %q (want repos|targets|groups|pool)", kind)
	}
	for _, name := range sortedUnique(out) {
		fmt.Fprintln(a.Out, name)
	}
	return nil
}

// startupPrompt implements the hidden `banshee _startup-prompt` subcommand the
// shell plugins call from their interactive init. It offers to restore the
// last action, but only when that would actually change anything: tmux must be
// installed, the prompt enabled, the shell interactive and outside tmux, and
// at least one of the last action's sessions not already running.
//
// The shell wrapper is expected to export BANSHEE_STARTUP_CHECKED=1 so nested
// shells stay quiet.
func (a *App) startupPrompt() error {
	if a.Builder == nil || !a.Builder.Available() {
		return nil
	}
	if !a.Cfg.StartupPrompt {
		return nil
	}
	if os.Getenv("TMUX") != "" || os.Getenv("BANSHEE_STARTUP_CHECKED") != "" {
		return nil
	}
	if !a.interactive() {
		return nil
	}

	last, err := a.State.Read()
	if err != nil {
		return nil // nothing recorded (or unreadable): stay silent
	}

	switch last.Kind {
	case state.KindTarget:
		if a.running(last.Name) {
			return nil
		}
	case state.KindGroup:
		g, err := session.LoadGroup(session.GroupPath(a.Res.GroupsDir, last.Name))
		if err != nil || len(g.Targets) == 0 {
			return nil
		}
		allRunning := true
		for _, t := range g.Targets {
			if !a.running(t) {
				allRunning = false
				break
			}
		}
		if allRunning {
			return nil
		}
	default:
		return nil
	}

	reply, err := a.prompt(fmt.Sprintf("banshee: restore last %s '%s'? [Y/n] ", last.Kind, last.Name))
	if err != nil {
		return nil
	}
	switch strings.TrimSpace(reply) {
	case "", "y", "Y", "yes", "YES", "Yes":
		return a.restore()
	}
	return nil
}
