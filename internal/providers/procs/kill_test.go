package procs

import (
	"errors"
	"reflect"
	"syscall"
	"testing"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

type killCall struct {
	pid int
	sig syscall.Signal
}

func TestRegisterKillHandler(t *testing.T) {
	tests := []struct {
		name      string
		action    providers.Action
		killErr   map[int]error
		wantCalls []killCall
		wantErr   bool
	}{
		{
			name:      "terminates every pid in the group",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"42", "101"}, Sig: syscall.SIGTERM},
			wantCalls: []killCall{{42, syscall.SIGTERM}, {101, syscall.SIGTERM}},
		},
		{
			name:      "alt action kills",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"7"}, Sig: syscall.SIGKILL},
			wantCalls: []killCall{{7, syscall.SIGKILL}},
		},
		{
			name:      "unset signal defaults to SIGTERM",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"7"}},
			wantCalls: []killCall{{7, syscall.SIGTERM}},
		},
		{
			name:      "already-exited process is not an error",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"1", "2"}},
			killErr:   map[int]error{1: syscall.ESRCH},
			wantCalls: []killCall{{1, syscall.SIGTERM}, {2, syscall.SIGTERM}},
		},
		{
			name:      "one failure does not stop the rest",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"1", "2"}},
			killErr:   map[int]error{1: syscall.EPERM},
			wantCalls: []killCall{{1, syscall.SIGTERM}, {2, syscall.SIGTERM}},
			wantErr:   true,
		},
		{
			name:      "invalid pid reported, valid pid still signalled",
			action:    providers.Action{Kind: ActKillProcs, Argv: []string{"abc", "9"}},
			wantCalls: []killCall{{9, syscall.SIGTERM}},
			wantErr:   true,
		},
		{
			name:    "no pids",
			action:  providers.Action{Kind: ActKillProcs},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []killCall
			d := launch.NewDispatcher()
			RegisterKillHandlerWith(d, func(pid int, sig syscall.Signal) error {
				calls = append(calls, killCall{pid, sig})
				return tt.killErr[pid]
			})
			err := d.Dispatch(tt.action)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if len(calls) != len(tt.wantCalls) || (len(calls) > 0 && !reflect.DeepEqual(calls, tt.wantCalls)) {
				t.Fatalf("calls = %+v, want %+v", calls, tt.wantCalls)
			}
		})
	}
}

func TestKillNoPIDsError(t *testing.T) {
	err := Kill(func(int, syscall.Signal) error { return nil }, providers.Action{Kind: ActKillProcs})
	if !errors.Is(err, ErrNoPIDs) {
		t.Fatalf("err = %v, want ErrNoPIDs", err)
	}
}

func TestDispatcherRejectsUnregisteredKind(t *testing.T) {
	d := launch.NewDispatcher()
	if err := d.Dispatch(providers.Action{Kind: ActKillProcs}); err == nil {
		t.Fatal("expected error before RegisterKillHandler")
	}
}
