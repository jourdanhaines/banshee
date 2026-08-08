package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/ipc"
)

// Default timings for EnsureDaemon. A cold GTK start is well under a second;
// three seconds is a generous ceiling before we report failure and point the
// user at the daemon log.
const (
	DefaultSpawnTimeout  = 3 * time.Second
	DefaultPollInterval  = 50 * time.Millisecond
	daemonSubcommandName = "daemon"
)

// SpawnFunc starts a detached daemon process. Injected by tests.
type SpawnFunc func() error

// PingFunc probes for a live daemon. It must report ipc.ErrNotRunning (wrapped
// is fine) when nothing is listening. Injected by tests.
type PingFunc func() (ipc.Response, error)

// EnsureOptions tunes EnsureDaemon. The zero value is the production setup:
// ping the real socket, spawn the real binary, poll every 50ms for 3s.
type EnsureOptions struct {
	Ping     PingFunc
	Spawn    SpawnFunc
	Timeout  time.Duration
	Interval time.Duration
	// Sleep waits between polls; nil means time.Sleep. Tests override it to
	// keep the polling loop instantaneous.
	Sleep func(time.Duration)
}

func (o EnsureOptions) withDefaults() EnsureOptions {
	if o.Ping == nil {
		o.Ping = ipc.Ping
	}
	if o.Spawn == nil {
		o.Spawn = SpawnDetached
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultSpawnTimeout
	}
	if o.Interval <= 0 {
		o.Interval = DefaultPollInterval
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	return o
}

// EnsureDaemon returns once a banshee daemon answers a ping, spawning one when
// none is running. This is what makes `banshee toggle` work straight from a
// Hyprland keybind with no systemd unit involved.
//
// Concurrent callers are safe: the loser of the flock race exits immediately
// and every client then finds the winner's socket.
func EnsureDaemon(opts EnsureOptions) error {
	o := opts.withDefaults()

	resp, lastErr := o.Ping()
	if lastErr == nil && resp.OK {
		return nil
	}
	// Only "nothing is listening" warrants a spawn. Any other answer means a
	// daemon exists but is not ready yet (GTK still starting), so we just wait
	// for it — starting a second one would lose the flock race anyway.
	if errors.Is(lastErr, ipc.ErrNotRunning) {
		if err := o.Spawn(); err != nil {
			return fmt.Errorf("spawn banshee daemon: %w", err)
		}
	}

	for waited := time.Duration(0); waited < o.Timeout; waited += o.Interval {
		o.Sleep(o.Interval)
		resp, err := o.Ping()
		if err == nil && resp.OK {
			return nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no response")
	}
	return fmt.Errorf("banshee daemon did not come up within %s (see %s): %w", o.Timeout, config.DaemonLogPath(), lastErr)
}

// Control is the client entry point behind `banshee toggle|show|hide|reload`:
// it makes sure a daemon exists and then sends the op. Ping and quit never
// auto-spawn — asking a dead daemon to quit is a no-op, not a reason to start
// one.
func Control(op, query string) (ipc.Response, error) {
	switch op {
	case ipc.OpPing, ipc.OpQuit:
		return ipc.Send(op, query)
	}
	if err := EnsureDaemon(EnsureOptions{}); err != nil {
		return ipc.Response{}, err
	}
	return ipc.Send(op, query)
}

// SpawnDetached starts `banshee daemon` in its own session with stdio wired to
// config.DaemonLogPath(), so the daemon outlives the short-lived process that
// launched it (a Hyprland keybind, a shell, a terminal that is about to close).
func SpawnDetached() error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "banshee" // resolved via $PATH
	}

	logFile, err := openDaemonLog()
	if err != nil {
		return err
	}
	defer logFile.Close()

	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer devNull.Close()

	cmd := exec.Command(exe, daemonSubcommandName)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	// Release the child: banshee never reaps it, init does.
	return cmd.Process.Release()
}

// openDaemonLog opens ~/.local/state/banshee/daemon.log for appending, creating
// the directory. It falls back to /dev/null so a read-only state dir can never
// stop the launcher from starting.
func openDaemonLog() (*os.File, error) {
	path := config.DaemonLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			return f, nil
		}
	}
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open daemon log: %w", err)
	}
	return f, nil
}
