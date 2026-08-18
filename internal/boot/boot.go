// Package boot assembles the launcher: it builds the provider registry, the
// aggregator, the action dispatcher and the plugin host, and hands them to
// internal/daemon together with a UI constructor.
//
// This is the one place that knows about every provider package, so adding a
// result category is a single Register call here plus the new package. It is
// deliberately separate from cmd/banshee (which stays dispatch-only) and from
// internal/cli (which must never import GTK).
package boot

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/daemon"
	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/hypr"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/ipc"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/notify"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/apps"
	"github.com/jourdanhaines/banshee/internal/providers/calc"
	"github.com/jourdanhaines/banshee/internal/providers/cliphist"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
	"github.com/jourdanhaines/banshee/internal/providers/lastaction"
	"github.com/jourdanhaines/banshee/internal/providers/plugins"
	"github.com/jourdanhaines/banshee/internal/providers/procs"
	"github.com/jourdanhaines/banshee/internal/providers/repos"
	"github.com/jourdanhaines/banshee/internal/providers/sessions"
	"github.com/jourdanhaines/banshee/internal/providers/steam"
	"github.com/jourdanhaines/banshee/internal/providers/totp"
	"github.com/jourdanhaines/banshee/internal/session"
	"github.com/jourdanhaines/banshee/internal/state"
	"github.com/jourdanhaines/banshee/internal/tmux"
	"github.com/jourdanhaines/banshee/internal/ui"
)

// Launcher owns the daemon-side object graph. Build it with New and run it
// with Run.
type Launcher struct {
	cfg  config.Config
	log  *log.Logger
	idx  *index.Scanner
	reg  *providers.Registry
	agg  *providers.ConcurrentAggregator
	disp *launch.Dispatcher

	// Providers holding reloadable state.
	host  *plugins.Host
	conn  *connectors.Provider
	apps  *apps.Provider
	steam *steam.Provider

	// totpSetup is the TOTP backend-failure record shared by the totp provider
	// (which renders the setup wizard from it) and the totp action handlers
	// (which record failures into it). One instance for the daemon's lifetime.
	totpSetup *totp.SetupState

	// clips is the session-only clipboard history shared by the watcher (which
	// appends), the cliphist provider (which reads) and the clip handlers
	// (which re-copy and delete). clipWatch owns the wl-paste child; it is
	// started by run — never by New, so building a Launcher in tests spawns
	// nothing — and cycled by reload when clipboard_history is toggled.
	clips     *cliphist.Store
	clipWatch *cliphist.Watcher

	// notifier sends plugin desktop notifications over the session bus. It
	// dials lazily, so constructing it here costs nothing; notifyOn gates it
	// and is flipped by reload when the `notifications` key changes —
	// atomically, because pluginNotify runs on plugin readLoop goroutines
	// while reload runs on the GTK main loop.
	notifier *notify.Notifier
	notifyOn atomic.Bool

	// win is the launcher window, created on the GTK main loop when the
	// application activates and only ever touched from that loop.
	win *ui.Launcher

	// bg serializes and joins the off-main-loop half of reload.
	bgMu sync.Mutex
	bg   sync.WaitGroup
}

// New builds the full provider stack from cfg. It never fails: a broken
// plugin, an unreadable desktop database or a missing repo cache degrades that
// one provider, not the launcher.
func New(cfg config.Config) *Launcher {
	logger := log.New(os.Stderr, "banshee: ", log.LstdFlags)

	b := &Launcher{
		cfg:  cfg,
		log:  logger,
		idx:  index.NewScanner(cfg),
		reg:  providers.NewRegistry(),
		disp: launch.NewDispatcher(),
	}

	// Warm the repo index from the cache file (rescanning when it is stale) so
	// the very first keystroke already has repos to match against.
	if err := b.idx.Refresh(); err != nil {
		logger.Printf("index: %v", err)
	}

	b.notifier = notify.New(notify.Options{Log: logger.Printf})
	b.notifyOn.Store(cfg.Notifications)

	b.host = plugins.NewHost(config.PluginsDir(), plugins.Options{
		Stderr: os.Stderr,
		Notify: b.pluginNotify,
	})
	if err := b.host.Load(); err != nil {
		logger.Printf("plugins: %v", err)
	}

	b.conn = connectors.New(b.idx, fuzzy.Score,
		connectors.WithCurrentRepo(connectors.TmuxCurrentRepo(tmux.ExecRunner{})))
	b.conn.AddManifests(b.host.URLManifests()...)

	b.apps = apps.New(fuzzy.Score, apps.WithMaxResults(cfg.MaxResults))
	b.steam = steam.New(fuzzy.Score, steam.WithMaxResults(cfg.MaxResults))

	// Created before the provider is registered, and never replaced: reload
	// re-runs registerHandlers, which rewires this same pointer into the
	// handlers' Deps, so a wizard raised before `banshee reload` is still the
	// one the already-registered provider reads afterwards. Allocating a fresh
	// state here (or in registerHandlers) would silently strand the provider on
	// the old instance and freeze the wizard on screen.
	b.totpSetup = &totp.SetupState{}

	// The store always exists and the provider is always registered (the
	// Registry has no removal); with the watcher off the store stays empty and
	// the provider renders nothing. Image capture needs the runtime dir — on
	// failure the history degrades to text-only rather than writing payloads
	// anywhere less private than tmpfs.
	var clipOpts []cliphist.StoreOption
	if dir, err := ipc.RuntimeDir(); err == nil {
		clipOpts = append(clipOpts, cliphist.WithImageDir(filepath.Join(dir, "clips")))
	} else {
		logger.Printf("cliphist: no runtime dir, image capture disabled: %v", err)
	}
	b.clips = cliphist.NewStore(clipOpts...)
	b.clipWatch = cliphist.NewWatcher(b.clips, cliphist.WatcherOptions{Log: logger.Printf})

	// Registration order is documentation only — the aggregator sorts by
	// (-Score, Category, Title). It is kept in category order so this list
	// reads the way the launcher looks.
	b.reg.Register(lastaction.New(state.Default()))
	b.reg.Register(sessions.New(b.idx, tmux.ExecRunner{}, config.SessionsDir()))
	b.reg.Register(b.conn)
	b.reg.Register(repos.New(b.idx))
	b.reg.Register(calc.New())
	b.reg.Register(totp.New(fuzzy.Score, totp.WithSetupState(b.totpSetup)))
	b.reg.Register(cliphist.New(b.clips, fuzzy.Score))
	b.reg.Register(b.apps)
	b.reg.Register(b.steam)
	b.reg.Register(procs.New(fuzzy.Score, procs.WithMaxResults(cfg.MaxResults)))
	// Exec plugins go through an indirection so `banshee reload` can swap the
	// whole set: the frozen Registry has no removal.
	b.reg.Register(&pluginSet{host: b.host})

	b.agg = providers.NewAggregator(b.reg, cfg.MaxResults)
	b.agg.Logger = logger

	b.registerHandlers()
	return b
}

// registerHandlers binds every action kind the registered providers can emit.
// Register replaces by kind, so this doubles as the reload path for a changed
// `terminal =`.
func (b *Launcher) registerHandlers() {
	opts := launch.Options{Terminal: b.cfg.Terminal}
	launch.RegisterBuiltins(b.disp, opts)
	apps.RegisterAppLaunchHandler(b.disp)
	procs.RegisterKillHandler(b.disp)
	plugins.RegisterCallbackHandler(b.disp, b.host)
	connectors.RegisterLinkHandler(b.disp)

	// The TOTP handlers read a vault off the main loop and report failures
	// after the window has hidden, so they get the UI notifier rather than
	// RegisterHandlers' log-only default. State and Reopen turn an unusable
	// backend from a dead-end toast into the in-launcher setup wizard: the
	// handler records the failure, then re-shows the launcher so the provider
	// can render it. Everything else (index path, backend resolution, clock)
	// keeps its production default.
	//
	// Copy is where TOTP codes are kept out of the clipboard history: the
	// store is told to drop the next capture matching the code's hash (only
	// the hash ever leaves the copy path), and the copy itself carries the
	// wl-copy sensitive hint so even a suppression miss lands masked.
	totp.RegisterHandlersWith(b.disp, totp.Deps{
		Copy: func(text string) error {
			b.clips.SuppressNext(sha256.Sum256([]byte(text)))
			return launch.CopyToClipboardSensitive(opts, text)
		},
		Notify: b.notifyUser,
		State:  b.totpSetup,
		Reopen: b.reopenLauncher,
	})

	// Clip re-copies run a clipboard tool round trip off the main loop and
	// report failures after the window has hidden, so they get the UI
	// notifier too; Reopen re-shows the pruned list after a delete.
	cliphist.RegisterHandlersWith(b.disp, cliphist.Deps{
		Store: b.clips,
		CopyText: func(text string, sensitive bool) error {
			if sensitive {
				return launch.CopyToClipboardSensitive(opts, text)
			}
			return launch.CopyToClipboard(opts, text)
		},
		CopyStream: func(mime string, r io.Reader, sensitive bool) error {
			return launch.CopyToClipboardMIME(opts, mime, r, sensitive)
		},
		Notify: b.notifyUser,
		Reopen: b.reopenLauncher,
	})

	// ActSession: attach in the most recently active tmux client (raising its
	// terminal via Hyprland when possible), else spawn a terminal running the
	// CLI. Ensure resolves with attach=false — the daemon has no TTY.
	runner := tmux.ExecRunner{}
	res := &session.Resolver{
		SessionsDir: config.SessionsDir(),
		GroupsDir:   config.GroupsDir(),
		Index:       b.idx,
		Builder:     tmux.NewBuilder(runner),
		Recorder:    state.Default(),
		Log:         b.log.Printf,
	}
	sessions.RegisterAttachHandler(b.disp, sessions.AttachOptions{
		Runner: runner,
		Ensure: func(target string) error {
			return res.Resolve(target, session.ModeDefault, false)
		},
		SpawnTerminal: func(target string) error {
			return b.disp.Dispatch(providers.Action{
				Kind: providers.ActTerminal,
				Argv: []string{sessions.SelfBinary(), target},
			})
		},
		Focus: (&hypr.Ctl{}).FocusTerminalOf,
		Log:   b.log.Printf,
	})
}

// pluginNotify is the plugin host's notification sink: it maps a plugin's
// wire notification onto a notify.Request and routes the daemon's
// action/close signals back to the plugin through respond. It runs on plugin
// readLoop goroutines and the notifier's signal goroutine — never the GTK
// main loop — which is why it talks to the bus directly instead of going
// anywhere near notifyUser.
func (b *Launcher) pluginNotify(pluginID string, n plugins.WireNotify, respond func(action string, closed bool, reason int)) {
	if !b.notifyOn.Load() {
		return
	}
	req := notify.Request{
		// Namespacing by plugin id keeps two plugins that both picked the
		// notification id "status" from replacing each other.
		Key:          pluginID + "/" + n.ID,
		Summary:      n.Summary,
		Body:         n.Body,
		Icon:         n.Icon,
		Urgency:      notify.ParseUrgency(n.Urgency),
		RequireInput: n.RequireInput,
		TimeoutMS:    n.TimeoutMS,
		SoundPath:    n.Sound,
		OnEvent: func(e notify.Event) {
			respond(e.ActionKey, e.Closed, e.Reason)
		},
	}
	for _, a := range n.Actions {
		req.Actions = append(req.Actions, notify.Action{Key: a.Key, Label: a.Label})
	}
	if err := b.notifier.Send(req); err != nil {
		b.log.Printf("notify: plugin %s: %v", pluginID, err)
	}
}

// notifyUser surfaces a message from an action handler that failed after
// Dispatch already returned — by then the window is hidden and there is no
// error left to propagate, so a desktop notification is the only channel back.
//
// It is called from detached handler goroutines, so the hop through
// glib.IdleAdd is mandatory: ui.Launcher touches GTK and GIO and must only be
// entered from the main loop. b.win is read inside that hop for the same
// reason — it is written there by newUI — and is nil for every message raised
// before the application activates, which falls back to the daemon log.
func (b *Launcher) notifyUser(msg string) {
	glib.IdleAdd(func() {
		if b.win == nil {
			b.log.Printf("%s", msg)
			return
		}
		b.win.Notify(msg)
	})
}

// reopenLauncher shows the launcher again on query. It is how a TOTP action
// handler that has recorded a backend failure gets the setup wizard in front of
// the user: Show resets the window to the results view and re-runs the query,
// so the provider re-renders and the wizard rows replace what was there.
//
// Like notifyUser it is called from detached handler goroutines — after
// Dispatch returned and the window hid — so the hop through glib.IdleAdd is
// mandatory, and b.win is read inside that hop because that is where newUI
// writes it. A message raised before the application activates finds no window
// and falls back to the daemon log.
//
// It deliberately calls b.win.Show rather than the reindexOnShow wrapper the
// daemon holds: that wrapper exists to rescan repos when the user summons the
// launcher, and a wizard reopen is banshee re-showing its own diagnosis, not a
// fresh search worth a filesystem walk.
func (b *Launcher) reopenLauncher(query string) {
	glib.IdleAdd(func() {
		if b.win == nil {
			b.log.Printf("totp: launcher not ready; cannot reopen with %q", query)
			return
		}
		b.win.Show(query)
	})
}

// Aggregator exposes the assembled aggregator (useful for an alternate
// frontend, and for tests that want to drive the real provider stack).
func (b *Launcher) Aggregator() providers.Aggregator { return b.agg }

// Dispatcher exposes the assembled action dispatcher.
func (b *Launcher) Dispatcher() *launch.Dispatcher { return b.disp }

// Run starts the daemon and blocks until it quits. It returns
// daemon.ErrAlreadyRunning when another daemon holds the lock.
func (b *Launcher) Run() error { return b.run(nil) }

// RunWithLock is Run for a caller that already holds the single-instance lock,
// which is how RunDaemon avoids building the whole launcher stack (including a
// full repo scan) only to discover another daemon owns the lock. Run releases
// the lock when it returns, whoever acquired it.
func (b *Launcher) RunWithLock(lock *daemon.Lock) error { return b.run(lock) }

func (b *Launcher) run(lock *daemon.Lock) error {
	// The clipboard watcher belongs to the daemon's lifetime, not the
	// Launcher's: starting it here (not in New) keeps tests that build the
	// object graph from spawning wl-paste. Start reports an environment where
	// history cannot work (no Wayland, no wl-clipboard); that is a log line,
	// not a failure — the launcher runs fine without it.
	if b.cfg.ClipboardHistory {
		if err := b.clipWatch.Start(); err != nil {
			b.log.Printf("%v", err)
		}
	}
	// Background plugins also belong to the daemon's lifetime: starting them
	// here (not in New) keeps tests and `banshee doctor` from spawning them,
	// and arms every future Host.Load to restart them.
	b.host.StartBackground()
	err := daemon.Run(daemon.Options{
		NewUI:    b.newUI,
		OnReload: b.reload,
		Logger:   b.log,
		Lock:     lock,
	})
	// Background reloads touch the index and the plugin host; join them before
	// tearing either down.
	b.waitBackground()
	// The plugin host owns child processes; they must not outlive the daemon.
	b.host.Shutdown()
	// After the host, so plugins have stopped pushing before the bus
	// connection and the action callbacks go away.
	b.notifier.Close()
	// The watcher owns the wl-paste child, and Clear drops the history's
	// runtime image files with the session that captured them.
	b.clipWatch.Shutdown()
	b.clips.Clear()
	return err
}

// newUI constructs the launcher window. Called once, on the GTK main loop.
func (b *Launcher) newUI(app *gtk.Application) daemon.UI {
	b.win = ui.NewLauncher(app, b.cfg, b.agg, b.disp)
	return &reindexOnShow{UI: b.win, idx: b.idx, log: b.log}
}

// reload is the daemon-level half of the reload op: re-read banshee.conf,
// force a repo rescan, rescan plugin manifests, refresh the desktop database.
// It runs on the GTK main loop just before ui.Reload.
//
// Only the work that *needs* the main loop happens here: parsing the config,
// restyling the window and re-reading the GIO desktop database. The repo
// rescan (a full filesystem walk) and the plugin restart (which tears down
// child processes and waits on them) are handed to a goroutine — running them
// inline froze the window for as long as they took and, past the daemon's
// dispatch timeout, made `banshee reload` report a failure for a reload that
// then succeeded anyway.
//
// Every step is attempted even when an earlier one failed, so one broken
// plugin cannot swallow a config change; the main-loop failures are joined
// into the reply the client sees, and the background ones are logged.
func (b *Launcher) reload() error {
	var errs []error

	if cfg, err := config.Load(); err != nil {
		errs = append(errs, fmt.Errorf("config: %w", err))
	} else {
		b.cfg = cfg
		b.idx.Configure(cfg.SearchPaths, cfg.MaxDepth, time.Duration(cfg.CacheTTL)*time.Second)
		b.agg.SetMaxResults(cfg.MaxResults)
		b.registerHandlers()
		if b.win != nil {
			b.win.SetConfig(cfg)
		}
		// clipboard_history toggles the watcher. Start on a running watcher is
		// a no-op, and Start after a crash-limit disable is the recovery path;
		// turning the key off also forgets everything captured so far — that
		// is what "off" means for a privacy switch. Shutdown joins the watcher
		// goroutine, but the child dies by SIGKILL so the wait is bounded.
		if cfg.ClipboardHistory {
			if err := b.clipWatch.Start(); err != nil {
				b.log.Printf("%v", err)
			}
		} else {
			b.clipWatch.Shutdown()
			b.clips.Clear()
		}
		// The gate is all a notifications toggle needs: background plugins
		// keep running either way, their pushes are just dropped at the sink.
		b.notifyOn.Store(cfg.Notifications)
	}

	if err := b.apps.Reload(); err != nil {
		errs = append(errs, fmt.Errorf("apps: %w", err))
	}
	// Like apps: a handful of tiny manifest reads, cheap enough for the main
	// loop.
	if err := b.steam.Reload(); err != nil {
		errs = append(errs, fmt.Errorf("steam: %w", err))
	}

	b.reloadBackground()
	return errors.Join(errs...)
}

// reloadBackground runs the blocking half of a reload off the GTK main loop.
// Calls are serialized so two reloads cannot rescan or restart plugins at the
// same time, and joinable via waitBackground.
func (b *Launcher) reloadBackground() {
	b.bg.Add(1)
	go func() {
		defer b.bg.Done()
		b.bgMu.Lock()
		defer b.bgMu.Unlock()

		if err := b.idx.Rescan(); err != nil {
			b.log.Printf("reload: index: %v", err)
		}
		if err := b.host.Load(); err != nil {
			b.log.Printf("reload: plugins: %v", err)
		}
		// Load() rebuilt the exec plugins — pluginSet reads through to the host
		// so there is nothing to re-register — but url manifests were copied
		// into the connector provider and must be replaced wholesale.
		// SetManifests takes the provider's own lock, so this is safe from here.
		b.conn.SetManifests(append(connectors.Builtins(), b.host.URLManifests()...))
	}()
}

// waitBackground blocks until every in-flight background reload has finished.
// Used by the daemon exit path and by tests, so a rescan can never outlive the
// process (or the test's temporary directories).
func (b *Launcher) waitBackground() { b.bg.Wait() }
