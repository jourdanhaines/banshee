package boot

import (
	"errors"
	"fmt"
	"sort"

	"github.com/jourdanhaines/banshee/internal/cli"
	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/daemon"
	"github.com/jourdanhaines/banshee/internal/ipc"
	"github.com/jourdanhaines/banshee/internal/providers/plugins"
)

// Hooks returns the launcher verbs for internal/cli.
//
// `banshee daemon` runs the launcher in this process; every other verb is a
// one-line JSON request over the control socket. toggle/show/hide/reload
// auto-spawn a daemon when none is listening, which is what makes the Hyprland
// bind (`$menu = banshee toggle`) work with no systemd unit involved.
func Hooks() cli.Hooks {
	return cli.Hooks{
		Daemon:  RunDaemon,
		Toggle:  func(query string) error { return control(ipc.OpToggle, query) },
		Show:    func(query string) error { return control(ipc.OpShow, query) },
		Hide:    func() error { return control(ipc.OpHide, "") },
		Reload:  func() error { return control(ipc.OpReload, "") },
		Quit:    quit,
		Plugins: pluginStatus,
	}
}

// pluginStatus scans ~/.config/banshee/plugins without starting anything: exec
// plugins are launched lazily on their first matching query, so `banshee
// doctor` only reads manifests. Load errors are per-plugin and non-fatal.
func pluginStatus() ([]string, error) {
	host := plugins.NewHost(config.PluginsDir(), plugins.Options{})
	loadErr := host.Load()
	defer host.Shutdown()

	var names []string
	for _, m := range host.URLManifests() {
		names = append(names, m.ID+" (url)")
	}
	for _, p := range host.ExecPlugins() {
		names = append(names, p.ID()+" (exec)")
	}
	sort.Strings(names)
	return names, loadErr
}

// RunDaemon builds the launcher and runs it in the foreground, blocking until
// it quits. A daemon already holding the lock is reported as success: the user
// asked for a running daemon and there is one.
//
// The single-instance lock is taken *before* the launcher is assembled. Two
// `banshee toggle` calls racing each other both spawn a daemon; the loser must
// exit immediately rather than pay for a full recursive repo scan and rewrite
// the shared repo_cache on its way out.
func RunDaemon() error {
	lockPath, err := ipc.LockPath()
	if err != nil {
		return err
	}
	lock, err := daemon.AcquireLock(lockPath)
	if err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			fmt.Println("banshee: daemon already running")
			return nil
		}
		return err
	}

	if err := config.EnsureDirs(); err != nil {
		lock.Release()
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		lock.Release()
		return err
	}
	// RunWithLock releases the lock when it returns.
	return New(cfg).RunWithLock(lock)
}

// control sends op, starting a daemon first when none is running.
func control(op, query string) error {
	_, err := daemon.Control(op, query)
	return err
}

// quit stops a running daemon. Asking a daemon that is not running to stop is
// not an error — the requested state (no daemon) already holds.
func quit() error {
	_, err := ipc.Send(ipc.OpQuit, "")
	if errors.Is(err, ipc.ErrNotRunning) {
		return nil
	}
	return err
}
