package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
)

// Detail enriches a Runner error with the diagnostics tmux wrote to stderr.
//
// A failed tmux invocation carries everything the user needs to fix it —
// "duplicate session: banshee", "no space for new pane", "can't find window",
// "no server running on /tmp/tmux-1000/default" — on stderr. exec.Cmd.Output
// captures those bytes into (*exec.ExitError).Stderr, but ExitError.Error()
// renders only "exit status 1", so wrapping the error with %w alone throws the
// useful half away.
//
// Detail is applied at the call sites in this package rather than inside
// ExecRunner so it works for any Runner that returns an *exec.ExitError. It
// returns err unchanged when there is nothing to add, and nil for nil.
func Detail(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	msg := bytes.TrimSpace(ee.Stderr)
	if len(msg) == 0 {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}
