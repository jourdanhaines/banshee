package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/ipc"
)

// pingScript replays a canned sequence of ping outcomes: one entry per call,
// with the last entry repeating forever.
type pingScript struct {
	errs  []error
	calls int
}

func (p *pingScript) ping() (ipc.Response, error) {
	i := p.calls
	p.calls++
	if i >= len(p.errs) {
		i = len(p.errs) - 1
	}
	if err := p.errs[i]; err != nil {
		return ipc.Response{}, err
	}
	return ipc.Response{OK: true, Version: "test"}, nil
}

func TestEnsureDaemon(t *testing.T) {
	notRunning := fmt.Errorf("%w (/run/banshee.sock)", ipc.ErrNotRunning)
	daemonAngry := errors.New("banshee daemon: launcher UI is not ready yet")
	spawnFailed := errors.New("permission denied")

	tests := []struct {
		name       string
		pings      []error
		spawnErr   error
		wantSpawns int
		wantPings  int
		wantErrSub string
	}{
		{
			name:       "already running: no spawn",
			pings:      []error{nil},
			wantSpawns: 0,
			wantPings:  1,
		},
		{
			name:       "spawns then comes up on the first poll",
			pings:      []error{notRunning, nil},
			wantSpawns: 1,
			wantPings:  2,
		},
		{
			name:       "spawns then comes up after a few polls",
			pings:      []error{notRunning, notRunning, notRunning, nil},
			wantSpawns: 1,
			wantPings:  4,
		},
		{
			name:       "never comes up",
			pings:      []error{notRunning},
			wantSpawns: 1,
			wantPings:  1 + 10, // initial probe + Timeout/Interval polls
			wantErrSub: "did not come up",
		},
		{
			name:       "spawn failure is reported",
			pings:      []error{notRunning},
			spawnErr:   spawnFailed,
			wantSpawns: 1,
			wantPings:  1,
			wantErrSub: "spawn banshee daemon",
		},
		{
			name:       "a daemon still starting up is waited for, not respawned",
			pings:      []error{daemonAngry, daemonAngry, nil},
			wantSpawns: 0,
			wantPings:  3,
		},
		{
			name:       "a daemon that never becomes ready is not respawned",
			pings:      []error{daemonAngry},
			wantSpawns: 0,
			wantPings:  1 + 10,
			wantErrSub: "not ready",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			script := &pingScript{errs: tt.pings}
			spawns := 0
			slept := 0

			err := EnsureDaemon(EnsureOptions{
				Ping: script.ping,
				Spawn: func() error {
					spawns++
					return tt.spawnErr
				},
				Timeout:  10 * time.Millisecond,
				Interval: time.Millisecond,
				Sleep:    func(time.Duration) { slept++ },
			})

			if tt.wantErrSub == "" {
				if err != nil {
					t.Fatalf("EnsureDaemon: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("EnsureDaemon succeeded, want error containing %q", tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErrSub)
				}
			}
			if spawns != tt.wantSpawns {
				t.Errorf("spawn calls = %d, want %d", spawns, tt.wantSpawns)
			}
			if script.calls != tt.wantPings {
				t.Errorf("ping calls = %d, want %d", script.calls, tt.wantPings)
			}
			if slept > script.calls {
				t.Errorf("slept %d times for %d pings; polling should sleep at most once per ping", slept, script.calls)
			}
		})
	}
}

// TestEnsureDaemonUsesRealDefaults checks the zero-value options fill in.
func TestEnsureDaemonDefaults(t *testing.T) {
	o := EnsureOptions{}.withDefaults()
	if o.Ping == nil || o.Spawn == nil || o.Sleep == nil {
		t.Fatal("withDefaults left a nil callback")
	}
	if o.Timeout != DefaultSpawnTimeout {
		t.Errorf("Timeout = %v, want %v", o.Timeout, DefaultSpawnTimeout)
	}
	if o.Interval != DefaultPollInterval {
		t.Errorf("Interval = %v, want %v", o.Interval, DefaultPollInterval)
	}
}

// TestEnsureDaemonNotRunningWrapping documents the contract EnsureDaemon relies
// on: a client dialling a dead socket reports ipc.ErrNotRunning.
func TestEnsureDaemonNotRunningWrapping(t *testing.T) {
	_, err := ipc.SendTo(t.TempDir()+"/definitely-absent.sock", ipc.Request{Op: ipc.OpPing})
	if !errors.Is(err, ipc.ErrNotRunning) {
		t.Fatalf("SendTo dead socket = %v, want ipc.ErrNotRunning", err)
	}
}
