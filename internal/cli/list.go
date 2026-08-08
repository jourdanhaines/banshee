package cli

import (
	"fmt"

	"github.com/jourdanhaines/banshee/internal/session"
	"github.com/jourdanhaines/banshee/internal/state"
)

// list prints every session config and group with its live tmux state,
// marking whichever one `banshee -r` would replay.
func (a *App) list() {
	last, _ := a.State.Read()

	targets := session.ListTargets(a.Res.SessionsDir)
	groups := session.ListGroups(a.Res.GroupsDir)

	if len(targets) > 0 {
		fmt.Fprintln(a.Out, "Targets:")
		for _, t := range targets {
			marker := ""
			if last.Kind == state.KindTarget && last.Name == t {
				marker = " (last)"
			}
			fmt.Fprintf(a.Out, "  %-24s [%s]%s\n", t, a.sessionState(t), marker)
		}
	}

	if len(groups) > 0 {
		if len(targets) > 0 {
			fmt.Fprintln(a.Out)
		}
		fmt.Fprintln(a.Out, "Groups:")
		for _, g := range groups {
			marker := ""
			if last.Kind == state.KindGroup && last.Name == g {
				marker = " (last)"
			}
			fmt.Fprintf(a.Out, "  %s%s\n", g, marker)

			cfg, err := session.LoadGroup(session.GroupPath(a.Res.GroupsDir, g))
			if err != nil {
				fmt.Fprintln(a.Out, "    (invalid group config)")
				continue
			}
			for _, t := range cfg.Targets {
				fmt.Fprintf(a.Out, "    %-22s [%s]\n", t, a.sessionState(t))
			}
		}
	}

	if len(targets) == 0 && len(groups) == 0 {
		fmt.Fprintln(a.Out, "banshee: no session configs or groups")
	}
}

// sessionState is "running" when the target's tmux session exists.
func (a *App) sessionState(target string) string {
	if a.running(target) {
		return "running"
	}
	return "stopped"
}

func (a *App) running(target string) bool {
	if a.Builder == nil || !a.Builder.Available() {
		return false
	}
	return a.Builder.HasSession(a.Builder.SessionName(target))
}
