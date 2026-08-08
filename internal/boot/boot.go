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
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/daemon"
	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/hypr"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/apps"
	"github.com/jourdanhaines/banshee/internal/providers/calc"
	"github.com/jourdanhaines/banshee/internal/providers/connectors"
	"github.com/jourdanhaines/banshee/internal/providers/lastaction"
	"github.com/jourdanhaines/banshee/internal/providers/plugins"
	"github.com/jourdanhaines/banshee/internal/providers/procs"
	"github.com/jourdanhaines/banshee/internal/providers/repos"
	"github.com/jourdanhaines/banshee/internal/providers/sessions"
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
	host *plugins.Host
	conn *connectors.Provider
	apps *apps.Provider

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

	b.host = plugins.NewHost(config.PluginsDir(), plugins.Options{Stderr: os.Stderr})
	if err := b.host.Load(); err != nil {
		logger.Printf("plugins: %v", err)
	}

	b.conn = connectors.New(b.idx, fuzzy.Score,
		connectors.WithCurrentRepo(connectors.TmuxCurrentRepo(tmux.ExecRunner{})))
	b.conn.AddManifests(b.host.URLManifests()...)

	b.apps = apps.New(fuzzy.Score, apps.WithMaxResults(cfg.MaxResults))

	// Registration order is documentation only — the aggregator sorts by
	// (-Score, Category, Title). It is kept in category order so this list
	// reads the way the launcher looks.
	b.reg.Register(lastaction.New(state.Default()))
	b.reg.Register(sessions.New(b.idx, tmux.ExecRunner{}, config.SessionsDir()))
	b.reg.Register(b.conn)
	b.reg.Register(repos.New(b.idx))
	b.reg.Register(calc.New())
	b.reg.Register(b.apps)
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
	launch.RegisterBuiltins(b.disp, launch.Options{Terminal: b.cfg.Terminal})
	apps.RegisterAppLaunchHandler(b.disp)
	procs.RegisterKillHandler(b.disp)
	plugins.RegisterCallbackHandler(b.disp, b.host)
	connectors.RegisterLinkHandler(b.disp)

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
	}

	if err := b.apps.Reload(); err != nil {
		errs = append(errs, fmt.Errorf("apps: %w", err))
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
