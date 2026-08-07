package sessions

import (
	"errors"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// attachRunner scripts tmux for the attach handler: has-session answers from
// live, list-clients returns clients, switch-client can be forced to fail.
type attachRunner struct {
	live      map[string]bool
	clients   string
	clientsOK bool
	switchErr error
	calls     [][]string
}

func (f *attachRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch args[0] {
	case "has-session":
		if f.live[strings.TrimPrefix(args[2], "=")] {
			return "", nil
		}
		return "", errors.New("no session")
	case "list-clients":
		if !f.clientsOK {
			return "", errors.New("no server")
		}
		return f.clients, nil
	case "switch-client":
		return "", f.switchErr
	}
	return "", nil
}

func (f *attachRunner) call(i int) string { return strings.Join(f.calls[i], " ") }

type attachHarness struct {
	runner  *attachRunner
	ensured []string
	spawned []string
	focused []int
	disp    *launch.Dispatcher
}

func newAttachHarness(t *testing.T, tweak func(*AttachOptions)) *attachHarness {
	t.Helper()
	h := &attachHarness{
		runner: &attachRunner{live: map[string]bool{}, clientsOK: true},
		disp:   launch.NewDispatcher(),
	}
	opts := AttachOptions{
		Runner:        h.runner,
		Ensure:        func(target string) error { h.ensured = append(h.ensured, target); return nil },
		SpawnTerminal: func(target string) error { h.spawned = append(h.spawned, target); return nil },
		Focus:         func(pid int) error { h.focused = append(h.focused, pid); return nil },
	}
	if tweak != nil {
		tweak(&opts)
	}
	RegisterAttachHandler(h.disp, opts)
	return h
}

func (h *attachHarness) dispatch(t *testing.T, target string, forceNew bool) error {
	t.Helper()
	return h.disp.Dispatch(providers.Action{
		Kind:     providers.ActSession,
		Target:   target,
		ForceNew: forceNew,
	})
}

const twoClients = "/dev/pts/3\t4200\t1700000100\n/dev/pts/7\t5100\t1700000900\n"

func TestAttachSwitchesMostRecentClient(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.live["demo"] = true
	h.runner.clients = twoClients

	if err := h.dispatch(t, "demo", false); err != nil {
		t.Fatal(err)
	}
	if got := h.runner.call(len(h.runner.calls) - 1); got != "switch-client -c /dev/pts/7 -t =demo" {
		t.Errorf("last tmux call = %q", got)
	}
	if len(h.focused) != 1 || h.focused[0] != 5100 {
		t.Errorf("focused = %v, want [5100]", h.focused)
	}
	if len(h.spawned) != 0 || len(h.ensured) != 0 {
		t.Errorf("spawned=%v ensured=%v, want none", h.spawned, h.ensured)
	}
}

func TestAttachEnsuresMissingSession(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.clients = twoClients

	if err := h.dispatch(t, "demo", false); err != nil {
		t.Fatal(err)
	}
	if len(h.ensured) != 1 || h.ensured[0] != "demo" {
		t.Errorf("ensured = %v, want [demo]", h.ensured)
	}
	if got := h.runner.call(len(h.runner.calls) - 1); got != "switch-client -c /dev/pts/7 -t =demo" {
		t.Errorf("last tmux call = %q", got)
	}
}

func TestAttachEnsureFailureIsFatal(t *testing.T) {
	boom := errors.New("no config or matching repo")
	h := newAttachHarness(t, func(o *AttachOptions) {
		o.Ensure = func(string) error { return boom }
	})
	if err := h.dispatch(t, "ghost", false); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if len(h.spawned) != 0 {
		t.Errorf("spawned = %v, want none after ensure failure", h.spawned)
	}
}

func TestAttachNoClientsSpawnsTerminal(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.live["demo"] = true
	h.runner.clients = ""

	if err := h.dispatch(t, "demo", false); err != nil {
		t.Fatal(err)
	}
	if len(h.spawned) != 1 || h.spawned[0] != "demo" {
		t.Errorf("spawned = %v, want [demo]", h.spawned)
	}
	if len(h.focused) != 0 {
		t.Errorf("focused = %v, want none", h.focused)
	}
}

func TestAttachSwitchFailureSpawnsTerminal(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.live["demo"] = true
	h.runner.clients = twoClients
	h.runner.switchErr = errors.New("lost server")

	if err := h.dispatch(t, "demo", false); err != nil {
		t.Fatal(err)
	}
	if len(h.spawned) != 1 || h.spawned[0] != "demo" {
		t.Errorf("spawned = %v, want [demo]", h.spawned)
	}
}

func TestAttachForceNewAlwaysSpawns(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.live["demo"] = true
	h.runner.clients = twoClients

	if err := h.dispatch(t, "demo", true); err != nil {
		t.Fatal(err)
	}
	if len(h.spawned) != 1 || h.spawned[0] != "demo" {
		t.Errorf("spawned = %v, want [demo]", h.spawned)
	}
	if len(h.runner.calls) != 0 {
		t.Errorf("tmux calls = %v, want none for ForceNew", h.runner.calls)
	}
}

func TestAttachSanitizesSessionName(t *testing.T) {
	h := newAttachHarness(t, nil)
	h.runner.live["my_repo"] = true
	h.runner.clients = twoClients

	if err := h.dispatch(t, "my.repo", false); err != nil {
		t.Fatal(err)
	}
	if got := h.runner.call(0); got != "has-session -t =my_repo" {
		t.Errorf("first tmux call = %q", got)
	}
}

func TestAttachEmptyTarget(t *testing.T) {
	h := newAttachHarness(t, nil)
	if err := h.dispatch(t, "", false); err == nil {
		t.Fatal("expected error for empty target")
	}
}

func TestPickClient(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		wantTTY string
		wantPID int
		wantOK  bool
	}{
		{"most recent wins", twoClients, "/dev/pts/7", 5100, true},
		{"single client", "/dev/pts/1\t99\t5\n", "/dev/pts/1", 99, true},
		{"empty output", "", "", 0, false},
		{"malformed lines skipped", "garbage\n/dev/pts/2\tnot-a-pid\t3\n/dev/pts/4\t70\t9\n", "/dev/pts/4", 70, true},
		{"all malformed", "one\ttwo\nthree\n", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tty, pid, ok := pickClient(tt.out)
			if tty != tt.wantTTY || pid != tt.wantPID || ok != tt.wantOK {
				t.Errorf("pickClient = (%q, %d, %v), want (%q, %d, %v)",
					tty, pid, ok, tt.wantTTY, tt.wantPID, tt.wantOK)
			}
		})
	}
}
