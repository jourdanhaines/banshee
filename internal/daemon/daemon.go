// Package daemon runs banshee's resident launcher process: a single-instance
// GTK application whose window is built once and toggled, driven by the
// newline-delimited JSON control socket in internal/ipc.
//
// Layout of the package:
//
//	daemon.go  lifecycle — flock, socket, GTK application, main-loop marshalling
//	ops.go     GTK-free protocol dispatch (toggle/show/hide/reload/ping/quit)
//	lock.go    single-instance advisory lock
//	ensure.go  client side — auto-spawn a daemon, then send an op
//
// The daemon never imports internal/ui: it takes a constructor for the UI
// interface so the frontend stays swappable and this package stays testable.
package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/ipc"
)

// AppID is banshee's GApplication identifier.
const AppID = "dev.jourdan.banshee"

// DefaultDispatchTimeout bounds how long the socket goroutine waits for the GTK
// main loop to answer an op before reporting the daemon as busy.
const DefaultDispatchTimeout = 5 * time.Second

// NewUIFunc builds the launcher UI. It is called once, on the GTK main loop,
// when the application activates. Integration passes ui.New here.
type NewUIFunc func(app *gtk.Application) UI

// Options configures Run. Only NewUI is required; every other field falls back
// to the production default.
type Options struct {
	// NewUI constructs the launcher window (required).
	NewUI NewUIFunc
	// AppID overrides the GApplication id. Defaults to AppID.
	AppID string
	// SocketPath overrides the control socket. Defaults to ipc.SocketPath().
	SocketPath string
	// LockPath overrides the single-instance lock. Defaults to ipc.LockPath().
	// Ignored when Lock is set.
	LockPath string
	// Lock is a single-instance lock the caller already acquired, so an
	// expensive launcher stack is only built once this process is known to be
	// the daemon. Run releases it on return either way. Optional.
	Lock *Lock
	// OnReload is the daemon-level half of the reload op: re-read banshee.conf,
	// reindex repos, rescan plugin manifests. Called on the GTK main loop just
	// before UI.Reload. Optional.
	OnReload func() error
	// Logger receives lifecycle lines. Defaults to log.Default().
	Logger *log.Logger
	// DispatchTimeout overrides DefaultDispatchTimeout.
	DispatchTimeout time.Duration
}

type daemon struct {
	opts    Options
	app     *gtk.Application
	timeout time.Duration
	log     *log.Logger
	// stopped is closed once the GTK main loop is gone, so requests that raced
	// shutdown fail fast instead of waiting out the dispatch timeout.
	stopped chan struct{}

	mu sync.Mutex
	ui UI
}

// Run starts the daemon and blocks until the application quits (a quit op, a
// SIGINT/SIGTERM, or GTK tearing down). It returns ErrAlreadyRunning when
// another daemon already holds the lock, in which case nothing is touched.
func Run(opts Options) error {
	if opts.NewUI == nil {
		return errors.New("daemon: Options.NewUI is required")
	}
	appID := opts.AppID
	if appID == "" {
		appID = AppID
	}
	sockPath, err := resolvePath(opts.SocketPath, ipc.SocketPath)
	if err != nil {
		return fmt.Errorf("daemon: resolve socket path: %w", err)
	}
	lockPath, err := resolvePath(opts.LockPath, ipc.LockPath)
	if err != nil {
		return fmt.Errorf("daemon: resolve lock path: %w", err)
	}
	timeout := opts.DispatchTimeout
	if timeout <= 0 {
		timeout = DefaultDispatchTimeout
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.Default()
	}

	// The lock is held for the whole lifetime; releasing it is what lets the
	// next daemon start, so it must outlive the socket and the main loop.
	lock := opts.Lock
	if lock == nil {
		lock, err = AcquireLock(lockPath)
		if err != nil {
			return err
		}
	}
	defer lock.Release()
	lockPath = lock.Path()

	d := &daemon{opts: opts, timeout: timeout, log: logger, stopped: make(chan struct{})}
	d.app = gtk.NewApplication(appID, gio.ApplicationNonUnique)
	d.app.ConnectActivate(func() {
		// Hold keeps the application alive with no window "open": banshee's
		// window is hidden, not destroyed, between toggles.
		d.app.Hold()
		d.setUI(opts.NewUI(d.app))
	})

	srv, err := ipc.Listen(sockPath, d.handle)
	if err != nil {
		return err
	}
	defer srv.Close()

	stopSignals := d.watchSignals()
	defer stopSignals()

	d.log.Printf("banshee %s daemon listening on %s (lock %s)", config.Version, sockPath, lockPath)

	// Pass only argv[0]: GApplication would try to parse "daemon" as its own
	// command line otherwise.
	code := d.app.Run([]string{os.Args[0]})
	close(d.stopped)
	if code != 0 {
		return fmt.Errorf("daemon: GTK application exited with status %d", code)
	}
	d.log.Printf("banshee daemon stopped")
	return nil
}

func resolvePath(explicit string, fallback func() (string, error)) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return fallback()
}

func (d *daemon) setUI(ui UI) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ui = ui
}

func (d *daemon) getUI() UI {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ui
}

// handle runs on the socket goroutine. GTK is not thread-safe, so the op is
// marshalled onto the main loop and the reply waits for its result.
func (d *daemon) handle(req ipc.Request) ipc.Response {
	done := make(chan ipc.Response, 1)
	glib.IdleAdd(func() {
		done <- handleOp(d.getUI(), req, d.opts.OnReload, d.quit)
	})
	select {
	case resp := <-done:
		return resp
	case <-d.stopped:
		return ipc.Response{Error: "daemon is shutting down"}
	case <-time.After(d.timeout):
		return ipc.Response{Error: fmt.Sprintf("daemon busy: main loop did not answer %q within %s", req.Op, d.timeout)}
	}
}

// quit is invoked from the main loop; the actual shutdown is deferred to a
// later idle callback so the current response reaches the client first.
func (d *daemon) quit() {
	glib.IdleAdd(func() { d.app.Quit() })
}

// watchSignals turns SIGINT/SIGTERM (systemd stop, Ctrl-C) into a clean GTK
// shutdown. The returned func stops watching.
func (d *daemon) watchSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	stop := make(chan struct{})
	go func() {
		select {
		case sig := <-ch:
			d.log.Printf("banshee daemon: %s received, shutting down", sig)
			d.quit()
		case <-stop:
		}
	}()
	return func() {
		signal.Stop(ch)
		close(stop)
	}
}
