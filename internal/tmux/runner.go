// Package tmux generates and executes tmux commands for banshee sessions.
// All tmux interaction goes through the Runner interface so tests can assert
// exact argv without a live tmux server.
//
// runner.go is a frozen Phase-0 contract.
package tmux

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes one tmux invocation and returns trimmed stdout.
type Runner interface {
	Run(args ...string) (string, error)
}

// ExecRunner runs the real tmux binary.
type ExecRunner struct{}

func (ExecRunner) Run(args ...string) (string, error) {
	out, err := exec.Command("tmux", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// Available reports whether tmux is on PATH.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// SessionName derives the tmux session name from a target: basename with
// '.' and ':' (tmux target separators) replaced by '_'. Exact port of the
// v0.3 behavior — the filename/target is authoritative, never JSON .name.
func SessionName(target string) string {
	name := filepath.Base(target)
	name = strings.ReplaceAll(name, ".", "_")
	name = strings.ReplaceAll(name, ":", "_")
	return name
}
