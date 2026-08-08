package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

// link implements `banshee link <connector-id> [path] [binding]`: resolve the
// repo root (path, or the git root above the current directory), take the
// binding from the arguments or a prompt, and write it through
// connectors.SaveRepoBinding — the same helper the launcher's link form
// dispatches to.
func (a *App) link(args []string) error {
	if len(args) < 1 || args[0] == "" {
		return fmt.Errorf("link requires a connector id (e.g. `banshee link railway`)")
	}
	id := args[0]
	// Shape-check only: the CLI has no plugin host, and refusing an id it
	// cannot enumerate would break plugin connectors.
	if !connectors.ValidIdentifier(id) {
		return fmt.Errorf("link: %q is not a valid connector id", id)
	}

	dir := ""
	if len(args) > 1 && args[1] != "" {
		dir = config.ExpandPath(args[1])
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	root, ok := connectors.FindGitRoot(dir)
	if !ok {
		return fmt.Errorf("link: %s is not inside a git repository", dir)
	}

	binding := ""
	if len(args) > 2 {
		binding = strings.TrimSpace(args[2])
	}
	if binding == "" {
		name := id
		for _, m := range connectors.Builtins() {
			if m.ID == id {
				name = m.Name
				break
			}
		}
		reply, err := a.prompt(name + " project URL or ID: ")
		if err != nil {
			return ErrCancelled
		}
		binding = strings.TrimSpace(reply)
		if binding == "" {
			return ErrCancelled
		}
	}

	if err := connectors.SaveRepoBinding(root, id, binding); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "banshee: linked %s in %s (%s)\n", id, root, config.RepoConfigRelPath)
	return nil
}
