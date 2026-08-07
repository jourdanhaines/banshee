package procs

import (
	"errors"
	"fmt"
	"strconv"
	"syscall"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// KillFunc sends sig to pid. It matches syscall.Kill so the real
// implementation needs no wrapper.
type KillFunc func(pid int, sig syscall.Signal) error

// ErrNoPIDs is returned when an ActKillProcs action carries no PIDs.
var ErrNoPIDs = errors.New("procs: kill-procs action carries no pids")

// RegisterKillHandler teaches d how to run ActKillProcs actions: every PID in
// Action.Argv is signalled with Action.Sig (SIGTERM when unset).
func RegisterKillHandler(d *launch.Dispatcher) {
	RegisterKillHandlerWith(d, syscall.Kill)
}

// RegisterKillHandlerWith is RegisterKillHandler with an injectable kill
// syscall, for tests.
func RegisterKillHandlerWith(d *launch.Dispatcher, kill KillFunc) {
	d.Register(ActKillProcs, func(a providers.Action) error {
		return Kill(kill, a)
	})
}

// Kill signals every PID carried by a. Processes that already exited are not
// an error; all other failures are collected so one permission denial does not
// hide the rest.
func Kill(kill KillFunc, a providers.Action) error {
	if len(a.Argv) == 0 {
		return ErrNoPIDs
	}
	sig := a.Sig
	if sig == 0 {
		sig = syscall.SIGTERM
	}
	var errs []error
	for _, s := range a.Argv {
		pid, err := strconv.Atoi(s)
		if err != nil || pid <= 0 {
			errs = append(errs, fmt.Errorf("procs: invalid pid %q", s))
			continue
		}
		if err := kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, fmt.Errorf("procs: kill %d: %w", pid, err))
		}
	}
	return errors.Join(errs...)
}
