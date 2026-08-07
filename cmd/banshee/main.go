// Command banshee is the single binary: a GTK4 layer-shell launcher for
// Hyprland and the tmux session CLI.
//
// main only dispatches. Terminal verbs go straight to internal/cli; launcher
// verbs (daemon, toggle, show, hide, reload, quit) are handed to the CLI as
// Hooks built by internal/boot, so internal/cli never imports GTK and stays
// testable without a display.
package main

import (
	"os"

	"github.com/jourdanhaines/banshee/internal/boot"
	"github.com/jourdanhaines/banshee/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], boot.Hooks()))
}
