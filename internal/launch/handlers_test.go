package launch

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// recorder captures what the handlers would have launched.
type recorder struct {
	argv []string
	pid  int
	sig  syscall.Signal
}

// testOptions returns Options that record instead of spawning, with a fake
// PATH containing only the named binaries.
func testOptions(rec *recorder, term string, onPath ...string) Options {
	path := map[string]bool{}
	for _, p := range onPath {
		path[p] = true
	}
	return Options{
		Terminal: term,
		Detach: func(argv []string) error {
			rec.argv = argv
			return nil
		},
		Kill: func(pid int, sig syscall.Signal) error {
			rec.pid, rec.sig = pid, sig
			return nil
		},
		LookPath: func(file string) (string, error) {
			if path[file] {
				return "/usr/bin/" + file, nil
			}
			return "", exec.ErrNotFound
		},
		Getenv: func(string) string { return "" },
	}
}

func TestBuiltinHandlers(t *testing.T) {
	tests := []struct {
		name     string
		action   providers.Action
		onPath   []string
		terminal string
		wantArgv []string
		wantErr  string
	}{
		{
			name:     "exec-detach passes argv through",
			action:   providers.Action{Kind: providers.ActExecDetach, Argv: []string{"firefox", "--new-window"}},
			wantArgv: []string{"firefox", "--new-window"},
		},
		{
			name:    "exec-detach rejects empty argv",
			action:  providers.Action{Kind: providers.ActExecDetach},
			wantErr: "empty argv",
		},
		{
			name:     "terminal wraps argv in the terminal",
			action:   providers.Action{Kind: providers.ActTerminal, Argv: []string{"banshee", "blacksheep"}},
			onPath:   []string{"kitty"},
			wantArgv: []string{"kitty", "-e", "banshee", "blacksheep"},
		},
		{
			name:     "terminal config override wins over the probe",
			action:   providers.Action{Kind: providers.ActTerminal, Argv: []string{"banshee"}},
			terminal: "wezterm",
			onPath:   []string{"wezterm", "kitty"},
			wantArgv: []string{"wezterm", "-e", "banshee"},
		},
		{
			name:    "terminal with nothing installed",
			action:  providers.Action{Kind: providers.ActTerminal, Argv: []string{"banshee"}},
			wantErr: "no terminal emulator found",
		},
		{
			name:     "url opens with xdg-open",
			action:   providers.Action{Kind: providers.ActURL, URL: "https://example.com"},
			wantArgv: []string{"xdg-open", "https://example.com"},
		},
		{
			name:    "url rejects empty",
			action:  providers.Action{Kind: providers.ActURL},
			wantErr: "empty URL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			d := NewDispatcher()
			RegisterBuiltins(d, testOptions(rec, tc.terminal, tc.onPath...))

			err := d.Dispatch(tc.action)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rec.argv, tc.wantArgv) {
				t.Errorf("argv = %v, want %v", rec.argv, tc.wantArgv)
			}
		})
	}
}

func TestSignalHandler(t *testing.T) {
	tests := []struct {
		name    string
		action  providers.Action
		wantSig syscall.Signal
		wantErr string
	}{
		{
			name:    "explicit signal",
			action:  providers.Action{Kind: providers.ActSignal, Pid: 4242, Sig: syscall.SIGKILL},
			wantSig: syscall.SIGKILL,
		},
		{
			name:    "zero signal defaults to SIGTERM",
			action:  providers.Action{Kind: providers.ActSignal, Pid: 4242},
			wantSig: syscall.SIGTERM,
		},
		{
			name:    "invalid pid",
			action:  providers.Action{Kind: providers.ActSignal, Pid: 0},
			wantErr: "invalid pid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			d := NewDispatcher()
			RegisterBuiltins(d, testOptions(rec, ""))
			err := d.Dispatch(tc.action)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if rec.pid != 4242 || rec.sig != tc.wantSig {
				t.Errorf("kill(%d, %v)", rec.pid, rec.sig)
			}
		})
	}
}

func TestResolveTerminalOrder(t *testing.T) {
	tests := []struct {
		name     string
		override string
		env      string
		onPath   []string
		want     string
		wantErr  bool
	}{
		{name: "config override", override: "ghostty", onPath: []string{"ghostty", "foot"}, want: "ghostty"},
		{name: "env when no override", env: "alacritty", onPath: []string{"alacritty", "foot"}, want: "alacritty"},
		{name: "override beats env", override: "kitty", env: "foot", onPath: []string{"kitty", "foot"}, want: "kitty"},
		{name: "missing override falls through to env", override: "nope", env: "foot", onPath: []string{"foot"}, want: "foot"},
		{name: "probe order", onPath: []string{"foot", "kitty"}, want: "kitty"},
		{name: "probe prefers ghostty", onPath: []string{"foot", "kitty", "ghostty"}, want: "ghostty"},
		{name: "nothing found", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := testOptions(&recorder{}, tc.override, tc.onPath...)
			opts.Getenv = func(key string) string {
				if key == "TERMINAL" {
					return tc.env
				}
				return ""
			}
			got, err := ResolveTerminal(opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ResolveTerminal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDispatchUnknownKind(t *testing.T) {
	d := NewDispatcher()
	RegisterBuiltins(d, testOptions(&recorder{}, ""))
	err := d.Dispatch(providers.Action{Kind: "teleport"})
	if err == nil || !strings.Contains(err.Error(), "no handler") {
		t.Fatalf("err = %v", err)
	}
}

func TestDetachEmptyArgv(t *testing.T) {
	if err := Detach(nil); err == nil {
		t.Fatal("expected error")
	}
}

// TestDetachSpawnsRealProcess is the regression test for the SysProcAttr
// combination Detach uses: with both Setsid and Setpgid set, the pre-exec
// child called setpgid(0,0) after setsid() and the kernel answered EPERM, so
// *every* detached launch (terminal rows, URL rows, exec-detach rows) failed
// before exec. Asserting on a real spawn is the only way to see that; the
// error-path tests below never reach fork.
func TestDetachSpawnsRealProcess(t *testing.T) {
	bin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no `true` binary on PATH")
	}
	if err := Detach([]string{bin}); err != nil {
		t.Fatalf("Detach(%s) = %v, want nil", bin, err)
	}
}

func TestDetachUnknownBinary(t *testing.T) {
	// No process is spawned: the lookup fails first, so this stays hermetic.
	err := Detach([]string{"definitely-not-a-real-binary-xyz"})
	if err == nil {
		t.Fatal("expected a launch error")
	}
	if !errors.Is(err, exec.ErrNotFound) && !strings.Contains(err.Error(), "could not launch") {
		t.Errorf("unexpected error: %v", err)
	}
}
