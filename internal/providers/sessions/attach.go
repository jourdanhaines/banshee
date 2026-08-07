package sessions

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/tmux"
)

// AttachOptions wires the ActSession handler. Runner is required; the rest
// degrade gracefully when nil.
type AttachOptions struct {
	// Runner executes tmux. Required.
	Runner tmux.Runner
	// Ensure brings the target's session up (built, not attached) when it is
	// not running yet — boot wires this to session.Resolver.Resolve with
	// attach=false. nil skips straight to SpawnTerminal.
	Ensure func(target string) error
	// SpawnTerminal opens a new terminal running `banshee <target>`. Required:
	// it is the fallback whenever no existing tmux client can be reused, and
	// the whole action for ForceNew.
	SpawnTerminal func(target string) error
	// Focus raises the terminal window owning a tmux client process
	// (internal/hypr). Best-effort: failures are logged, never fatal. May be
	// nil.
	Focus func(clientPid int) error
	// Log receives non-fatal progress messages. May be nil.
	Log func(format string, args ...any)
}

// RegisterAttachHandler binds providers.ActSession: attach the action's
// Target in the most recently active tmux client — switching that client to
// the session and raising its terminal window — or spawn a fresh terminal
// when there is no client to reuse (or the action demands one via ForceNew).
func RegisterAttachHandler(d *launch.Dispatcher, opts AttachOptions) {
	d.Register(providers.ActSession, func(a providers.Action) error {
		return attachSession(a, opts)
	})
}

func attachSession(a providers.Action, opts AttachOptions) error {
	if a.Target == "" {
		return errors.New("session action has no target")
	}
	if opts.SpawnTerminal == nil {
		return errors.New("session action has no terminal fallback")
	}
	if a.ForceNew || opts.Runner == nil {
		return opts.SpawnTerminal(a.Target)
	}

	name := tmux.SessionName(a.Target)

	// Bring the session up first so switch-client has somewhere to land.
	// Ensure builds detached (the daemon has no TTY to attach).
	if _, err := opts.Runner.Run("has-session", "-t", "="+name); err != nil {
		if opts.Ensure == nil {
			return opts.SpawnTerminal(a.Target)
		}
		if err := opts.Ensure(a.Target); err != nil {
			return err
		}
	}

	out, err := opts.Runner.Run("list-clients", "-F", "#{client_tty}\t#{client_pid}\t#{client_activity}")
	if err != nil {
		return opts.SpawnTerminal(a.Target)
	}
	tty, pid, ok := pickClient(out)
	if !ok {
		// tmux server up, but nothing attached anywhere — new terminal.
		return opts.SpawnTerminal(a.Target)
	}

	if _, err := opts.Runner.Run("switch-client", "-c", tty, "-t", "="+name); err != nil {
		return opts.SpawnTerminal(a.Target)
	}
	if opts.Focus != nil {
		if err := opts.Focus(pid); err != nil && opts.Log != nil {
			opts.Log("focus terminal of tmux client %d: %v", pid, err)
		}
	}
	return nil
}

// pickClient parses `list-clients -F '#{client_tty}\t#{client_pid}\t#{client_activity}'`
// output and returns the tty and pid of the most recently active client —
// "the last active terminal". Malformed lines are skipped; ok is false when
// no usable client remains.
func pickClient(out string) (tty string, pid int, ok bool) {
	type client struct {
		tty      string
		pid      int
		activity int64
	}
	var clients []client
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 3 || fields[0] == "" {
			continue
		}
		p, err := strconv.Atoi(fields[1])
		if err != nil || p <= 0 {
			continue
		}
		act, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		clients = append(clients, client{tty: fields[0], pid: p, activity: act})
	}
	if len(clients) == 0 {
		return "", 0, false
	}
	sort.SliceStable(clients, func(i, j int) bool { return clients[i].activity > clients[j].activity })
	return clients[0].tty, clients[0].pid, true
}
