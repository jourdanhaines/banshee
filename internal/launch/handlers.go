package launch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// TerminalCandidates are probed, in order, when neither the config nor
// $TERMINAL names a terminal emulator.
var TerminalCandidates = []string{"ghostty", "kitty", "alacritty", "foot"}

// Options configures the built-in handlers. The function fields exist so
// tests can observe what would be launched without spawning anything; all of
// them may be left nil.
type Options struct {
	// Terminal is the config override (banshee.conf `terminal =`). It wins
	// over $TERMINAL and the probe list.
	Terminal string
	// Detach starts argv as a detached process. nil uses Detach.
	Detach func(argv []string) error
	// Kill sends a signal to a pid. nil uses syscall.Kill.
	Kill func(pid int, sig syscall.Signal) error
	// LookPath resolves a binary name. nil uses exec.LookPath.
	LookPath func(file string) (string, error)
	// Getenv reads the environment. nil uses os.Getenv.
	Getenv func(key string) string
	// RunStdin runs argv to completion with stdin fed from r. nil uses a
	// real command run bounded by clipboardTimeout. Used by clipboard-copy.
	RunStdin func(argv []string, stdin io.Reader) error
}

func (o Options) lookPath(file string) (string, error) {
	if o.LookPath != nil {
		return o.LookPath(file)
	}
	return exec.LookPath(file)
}

func (o Options) getenv(key string) string {
	if o.Getenv != nil {
		return o.Getenv(key)
	}
	return os.Getenv(key)
}

func (o Options) detach(argv []string) error {
	if o.Detach != nil {
		return o.Detach(argv)
	}
	return Detach(argv)
}

func (o Options) runStdin(argv []string, stdin io.Reader) error {
	if o.RunStdin != nil {
		return o.RunStdin(argv, stdin)
	}
	return runStdinCmd(argv, stdin)
}

func (o Options) kill(pid int, sig syscall.Signal) error {
	if o.Kill != nil {
		return o.Kill(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

// RegisterBuiltins registers the handlers for every action kind banshee ships
// with: exec-detach, terminal, url, signal and clipboard-copy. Plugin-callback
// actions are registered by the plugin host.
func RegisterBuiltins(d *Dispatcher, opts Options) {
	d.Register(providers.ActExecDetach, func(a providers.Action) error {
		if len(a.Argv) == 0 {
			return errors.New("exec-detach: empty argv")
		}
		return opts.detach(a.Argv)
	})

	d.Register(providers.ActTerminal, func(a providers.Action) error {
		if len(a.Argv) == 0 {
			return errors.New("terminal: empty argv")
		}
		term, err := ResolveTerminal(opts)
		if err != nil {
			return err
		}
		argv := append([]string{term, "-e"}, a.Argv...)
		return opts.detach(argv)
	})

	d.Register(providers.ActURL, func(a providers.Action) error {
		if a.URL == "" {
			return errors.New("url: empty URL")
		}
		return opts.detach([]string{"xdg-open", a.URL})
	})

	d.Register(providers.ActClipboardCopy, func(a providers.Action) error {
		if a.Text == "" {
			return errors.New("clipboard-copy: empty text")
		}
		return CopyToClipboard(opts, a.Text)
	})

	d.Register(providers.ActSignal, func(a providers.Action) error {
		if a.Pid <= 0 {
			return fmt.Errorf("signal: invalid pid %d", a.Pid)
		}
		sig := a.Sig
		if sig == 0 {
			sig = syscall.SIGTERM
		}
		return opts.kill(a.Pid, sig)
	})
}

// ResolveTerminal picks the terminal emulator to spawn: the config override
// first, then $TERMINAL, then the first of TerminalCandidates found on PATH.
func ResolveTerminal(opts Options) (string, error) {
	for _, cand := range []string{opts.Terminal, opts.getenv("TERMINAL")} {
		if cand == "" {
			continue
		}
		if _, err := opts.lookPath(cand); err == nil {
			return cand, nil
		}
	}
	for _, cand := range TerminalCandidates {
		if _, err := opts.lookPath(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("no terminal emulator found (set `terminal =` in banshee.conf or $TERMINAL; tried %v)", TerminalCandidates)
}

// Detach starts argv in its own session (and therefore its own process group)
// with stdio wired to /dev/null, so the child outlives banshee and never
// writes to the launcher's terminal.
func Detach(argv []string) error {
	if len(argv) == 0 {
		return errors.New("detach: empty argv")
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	// Setsid only. It already makes the child a session *and* process-group
	// leader; asking for Setpgid as well makes the pre-exec child call
	// setpgid(0,0) after setsid(), which the kernel rejects with EPERM for a
	// session leader — every spawn would fail before exec.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not launch %s: %w", argv[0], err)
	}
	// Detached: never wait, just let go of the process handle.
	return cmd.Process.Release()
}
