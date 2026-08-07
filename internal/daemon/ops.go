package daemon

import (
	"fmt"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/ipc"
)

// UI is the launcher window as the daemon sees it. internal/ui provides the
// real implementation; the daemon never imports it, so an alternative frontend
// (a TUI, a test double) only has to satisfy these four methods.
//
// Every method is called on the GTK main loop, never from the socket goroutine.
type UI interface {
	// Show reveals the launcher, prefilling the search box with query
	// (empty query = fresh, cleared entry).
	Show(query string)
	// Hide hides the launcher without destroying it, keeping toggles warm.
	Hide()
	// Visible reports whether the launcher is currently on screen.
	Visible() bool
	// Reload re-reads configuration and re-renders (theme, providers, plugins).
	Reload()
}

// handleOp executes one protocol op against the UI. It is deliberately free of
// GTK: Run wraps it in a main-loop callback, and tests drive it directly.
//
// ui may be nil while the GTK application is still starting up: ping reports
// that as not-ready and quit still works, everything else is refused.
// onReload and quit may be nil.
func handleOp(ui UI, req ipc.Request, onReload func() error, quit func()) ipc.Response {
	switch req.Op {
	case ipc.OpPing:
		// Ping doubles as the readiness probe EnsureDaemon polls, so it only
		// reports ok once the launcher can actually service a toggle.
		if ui == nil {
			return ipc.Response{Version: config.Version, Error: "launcher UI is not ready yet"}
		}
		return ipc.Response{OK: true, Version: config.Version, Visible: ui.Visible()}
	case ipc.OpQuit:
		if quit != nil {
			quit()
		}
		return ipc.Response{OK: true, Version: config.Version}
	}

	if ui == nil {
		return ipc.Response{Error: "launcher UI is not ready yet"}
	}

	switch req.Op {
	case ipc.OpToggle:
		if ui.Visible() {
			ui.Hide()
		} else {
			ui.Show(req.Query)
		}
	case ipc.OpShow:
		ui.Show(req.Query)
	case ipc.OpHide:
		ui.Hide()
	case ipc.OpReload:
		// Daemon-level state first (config, repo index, plugin manifests), then
		// the UI, so the redraw already sees the new state. A failing reload
		// hook is reported but still leaves the UI refreshed.
		var err error
		if onReload != nil {
			err = onReload()
		}
		ui.Reload()
		if err != nil {
			return ipc.Response{Error: "reload: " + err.Error()}
		}
	default:
		return ipc.Response{Error: fmt.Sprintf("unknown op %q", req.Op)}
	}
	return ipc.Response{OK: true}
}
