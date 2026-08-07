// Package cli implements banshee's terminal interface: the v0.3 flag set
// (-s/-se/-g/-ge/-r/-l/-c/-v/-h plus a bare target or query), the repo picker,
// the session-config editor loop, the hidden shell-integration subcommands and
// `banshee doctor`.
//
// The package deliberately does not import the daemon or GTK: launcher verbs
// are handed in as Hooks by cmd/banshee, so the CLI stays buildable and
// testable without a display.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/session"
	"github.com/jourdanhaines/banshee/internal/state"
	"github.com/jourdanhaines/banshee/internal/tmux"
)

// Hooks are the launcher entry points wired in by cmd/banshee. Every field
// may be nil; the corresponding verb then reports that the launcher is not
// available in this build.
type Hooks struct {
	// Daemon runs the launcher daemon in the foreground.
	Daemon func() error
	// Toggle shows or hides the launcher window, spawning the daemon when
	// it is not running. query optionally prefills the search box.
	Toggle func(query string) error
	// Show reveals the launcher window.
	Show func(query string) error
	// Hide hides the launcher window.
	Hide func() error
	// Reload makes the daemon re-read config, repos and plugins.
	Reload func() error
	// Quit stops the daemon.
	Quit func() error
	// Plugins reports the loaded plugin names and any per-plugin load errors,
	// for `banshee doctor`. A nil hook omits the plugin section entirely.
	Plugins func() (names []string, err error)
}

// App holds everything one CLI invocation needs. Construct it with New (or
// build it by hand in tests, where every dependency is an interface).
type App struct {
	Cfg     config.Config
	Index   index.Index
	Builder *tmux.Builder
	State   *state.Store
	Res     *session.Resolver
	Hooks   Hooks

	In  io.Reader
	Out io.Writer
	Err io.Writer

	// Interactive reports whether stdin and stdout are a terminal. nil
	// probes the real file descriptors.
	Interactive func() bool
	// HasFzf reports whether the fzf picker is available. nil probes $PATH.
	HasFzf func() bool

	reader *bufio.Reader
}

// Run builds an App from the environment and executes args (which must not
// include the program name). It returns the process exit code.
func Run(args []string, h Hooks) int {
	app, err := New(h)
	if err != nil {
		fmt.Fprintf(os.Stderr, "banshee: %v\n", err)
		return 1
	}
	return app.Run(args)
}

// New builds the default App: config from ~/.config/banshee/banshee.conf,
// the filesystem repo index, the real tmux runner and stdio.
func New(h Hooks) (*App, error) {
	if err := config.EnsureDirs(); err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	app := &App{
		Cfg:   cfg,
		Index: index.NewScanner(cfg),
		State: state.Default(),
		Hooks: h,
		In:    os.Stdin,
		Out:   os.Stdout,
		Err:   os.Stderr,
	}
	app.State.Migrate(config.DataDir())
	app.Builder = &tmux.Builder{
		R:          tmux.ExecRunner{},
		AttachFunc: tmux.ExecAttach,
		Log:        app.logf,
	}
	app.Res = &session.Resolver{
		SessionsDir: config.SessionsDir(),
		GroupsDir:   config.GroupsDir(),
		Index:       app.Index,
		Builder:     app.Builder,
		Recorder:    app.State,
		EditSession: func(target string) error { return app.EditSession(target, true) },
		Log:         app.logf,
	}
	return app, nil
}

// Run executes one command line and returns the process exit code.
func (a *App) Run(args []string) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "-h", "--help":
		a.usage(a.Out)
		return 0

	case "-v", "--version":
		fmt.Fprintf(a.Out, "banshee %s\n", config.Version)
		return 0

	case "-r", "--restore":
		return a.code(a.restore())

	case "-s", "--session":
		if len(args) < 2 || args[1] == "" {
			return a.fail("-s requires a target name")
		}
		return a.code(a.Res.Resolve(args[1], session.ModeRequireConfig, true))

	case "-se", "--edit-session":
		if len(args) < 2 || args[1] == "" {
			return a.fail("-se requires a target name")
		}
		return a.code(a.EditSession(args[1], false))

	case "-g", "--group":
		if len(args) < 2 || args[1] == "" {
			return a.fail("-g requires a group name")
		}
		return a.code(a.LoadGroup(args[1]))

	case "-ge", "--edit-group":
		if len(args) < 2 || args[1] == "" {
			return a.fail("-ge requires a group name")
		}
		return a.code(a.EditGroup(args[1]))

	case "-l", "--list":
		a.list()
		return 0

	case "-c", "--clear":
		if err := a.Index.Clear(); err != nil {
			return a.code(err)
		}
		fmt.Fprintln(a.Out, "banshee: cache cleared")
		return 0

	case "doctor":
		return a.code(a.doctor())

	case "_complete":
		return a.code(a.complete(args[1:]))

	case "_startup-prompt":
		return a.code(a.startupPrompt())

	case "_pick-repo":
		query := ""
		if len(args) > 1 {
			query = args[1]
		}
		return a.code(a.pickRepo(query))

	case "daemon":
		return a.code(a.hook("daemon", a.Hooks.Daemon))
	case "toggle":
		return a.code(a.hookQuery("toggle", a.Hooks.Toggle, args[1:]))
	case "show":
		return a.code(a.hookQuery("show", a.Hooks.Show, args[1:]))
	case "hide":
		return a.code(a.hook("hide", a.Hooks.Hide))
	case "reload":
		return a.code(a.hook("reload", a.Hooks.Reload))
	case "quit":
		return a.code(a.hook("quit", a.Hooks.Quit))
	}

	if strings.HasPrefix(cmd, "-") {
		fmt.Fprintf(a.Err, "banshee: unknown option '%s'\n", cmd)
		a.usage(a.Err)
		return 1
	}

	return a.code(a.open(cmd))
}

// open is the bare `banshee [query]` flow: an exact target-config or repo
// match loads straight away, anything else goes through the picker.
func (a *App) open(query string) error {
	target := ""
	if query != "" {
		if fileExists(session.SessionPath(a.Res.SessionsDir, query)) {
			target = query
		} else if _, ok := a.Index.Exact(query); ok {
			target = query
		} else if a.Builder != nil && a.Builder.Available() &&
			a.Builder.HasSession(a.Builder.SessionName(query)) {
			// A running tmux session that is neither a config target nor a
			// repo (e.g. a bare `tmux new` session) — attach to it directly.
			target = query
		}
	}
	if target == "" {
		selected, err := a.SelectRepo(query)
		if err != nil {
			// Esc is how the picker is normally dismissed, and this is the
			// Ctrl-F flow: the widget redraws the prompt straight after, so a
			// "banshee: cancelled" line above it would be pure noise. Legacy
			// was silent here too.
			if errors.Is(err, ErrCancelled) {
				return nil
			}
			return err
		}
		target = baseName(selected)
	}
	return a.Res.Resolve(target, session.ModeDefault, true)
}

// pickRepo implements the hidden `banshee _pick-repo [query]` subcommand: it
// runs the repo picker and prints the chosen path on stdout.
//
// It exists for the shell plugins' no-tmux fallback. A binary cannot change
// its parent's working directory, so when tmux is missing the shell function
// asks banshee which repo the user picked and cds there itself — which is what
// the v0.3 `banshee()` wrapper did inline. A cancelled picker prints nothing
// and succeeds, so the shell simply stays put.
func (a *App) pickRepo(query string) error {
	path, err := a.SelectRepo(query)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			return nil
		}
		return err
	}
	fmt.Fprintln(a.Out, path)
	return nil
}

// restore replays the last action recorded in last_action.
func (a *App) restore() error {
	return a.State.Restore(
		func(name string) error { return a.Res.Resolve(name, session.ModeDefault, true) },
		func(name string) error { return a.LoadGroup(name) },
	)
}

// LoadGroup loads a group, prompting for a target multi-select when the group
// config does not exist yet.
func (a *App) LoadGroup(name string) error {
	err := a.Res.ResolveGroup(name, true)
	if err == nil || !isGroupMissing(err) {
		return err
	}
	targets, err := a.SelectTargets(name, nil)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			return errors.New("group creation cancelled")
		}
		return err
	}
	if err := session.WriteGroup(a.Res.GroupsDir, name, targets); err != nil {
		return err
	}
	return a.Res.ResolveGroup(name, true)
}

// EditGroup rewrites a group's target list through the multi-select prompt.
func (a *App) EditGroup(name string) error {
	var current []string
	if g, err := session.LoadGroup(session.GroupPath(a.Res.GroupsDir, name)); err == nil {
		current = g.Targets
	}
	targets, err := a.SelectTargets(name, current)
	if err != nil {
		if errors.Is(err, ErrCancelled) {
			return errors.New("edit cancelled")
		}
		return err
	}
	if err := session.WriteGroup(a.Res.GroupsDir, name, targets); err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "banshee: group '%s' saved\n", name)
	return nil
}

func (a *App) hook(name string, fn func() error) error {
	if fn == nil {
		return fmt.Errorf("%s: launcher not available in this build", name)
	}
	return fn()
}

func (a *App) hookQuery(name string, fn func(string) error, rest []string) error {
	if fn == nil {
		return fmt.Errorf("%s: launcher not available in this build", name)
	}
	query := ""
	if len(rest) > 0 {
		query = strings.Join(rest, " ")
	}
	return fn(query)
}

// code turns an error into an exit code, printing it first.
func (a *App) code(err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(a.Err, "banshee: %v\n", err)
	return 1
}

func (a *App) fail(msg string) int {
	fmt.Fprintf(a.Err, "banshee: %s\n", msg)
	return 1
}

func (a *App) logf(format string, args ...any) {
	fmt.Fprintf(a.Err, "banshee: "+format+"\n", args...)
}

// interactive reports whether we can prompt the user.
func (a *App) interactive() bool {
	if a.Interactive != nil {
		return a.Interactive()
	}
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

// prompt writes msg to stderr and reads one line from stdin. The reader is
// kept on the App so repeated prompts (the editor loop) do not lose buffered
// input.
func (a *App) prompt(msg string) (string, error) {
	fmt.Fprint(a.Err, msg)
	if a.reader == nil {
		in := a.In
		if in == nil {
			in = os.Stdin
		}
		a.reader = bufio.NewReader(in)
	}
	line, err := a.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (a *App) usage(w io.Writer) {
	fmt.Fprint(w, usageText)
}

const usageText = `banshee - launcher + fluid git repo navigation + declarative tmux sessions

Usage:
  banshee [query]         repo picker → load target (config if defined, else plain)
  banshee <target>        Load target directly (exact-match)
  banshee -s <target>     Load target; if no config, open $EDITOR to create one
  banshee -se <target>    Edit (or create) target session config; no load
  banshee -g <name>       Load group; if missing, prompt multi-select to create
  banshee -ge <name>      Edit (or create) group via multi-select; no load
  banshee -r              Re-run last action (target or group)
  banshee -l              List session configs and groups
  banshee -c              Clear repository cache
  banshee -v              Show version
  banshee -h              Show this help

Launcher:
  banshee toggle [query]  Show/hide the launcher window (spawns the daemon)
  banshee show [query]    Show the launcher window
  banshee hide            Hide the launcher window
  banshee daemon          Run the launcher daemon in the foreground
  banshee reload          Re-read config, repos and plugins
  banshee quit            Stop the daemon
  banshee doctor          Diagnose the installation

Configs: ~/.config/banshee/sessions/<target>.json  (per-target)
Groups:  ~/.config/banshee/groups/<name>.json
Config:  ~/.config/banshee/banshee.conf
Optional: fzf (nicer pickers), tmux (session features)

`

func isGroupMissing(err error) bool { return errors.Is(err, session.ErrGroupMissing) }

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func baseName(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
