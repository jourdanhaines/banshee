package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jourdanhaines/banshee/internal/session"
)

// EditorCandidates are probed, in order, when neither $EDITOR nor $VISUAL is
// set.
var EditorCandidates = []string{"nvim", "vim", "nano", "vi"}

// EditSession opens a target's session config in the user's editor, creating
// it from the default template first if needed, and re-validates after every
// save: an invalid config offers "reopen editor" or "cancel". A config that
// was created by this call is removed again when the user cancels, so a failed
// edit leaves no half-written file behind.
//
// When load is true the target is loaded (and attached to) once the config
// validates — the `banshee -s <target>` flow.
func (a *App) EditSession(target string, load bool) error {
	if target == "" {
		return errors.New("empty target")
	}
	dir := a.Res.SessionsDir
	created, err := session.WriteTemplate(dir, target)
	if err != nil {
		return err
	}
	cfg := session.SessionPath(dir, target)

	editor, err := ResolveEditor()
	if err != nil {
		if created {
			_ = os.Remove(cfg)
		}
		return err
	}

	for {
		if err := a.runEditor(editor, cfg); err != nil {
			if created {
				_ = os.Remove(cfg)
			}
			return err
		}
		if _, err := session.LoadSession(cfg); err == nil {
			break
		} else {
			fmt.Fprintf(a.Err, "banshee: %v\n", err)
		}

		reply, err := a.prompt("banshee: config invalid. [r]eopen editor / [c]ancel? ")
		if err != nil {
			if created {
				_ = os.Remove(cfg)
			}
			return ErrCancelled
		}
		switch strings.TrimSpace(reply) {
		case "", "r", "R":
			continue
		default:
			if created {
				_ = os.Remove(cfg)
			}
			return ErrCancelled
		}
	}

	if load {
		return a.Res.Resolve(target, session.ModeDefault, true)
	}
	return nil
}

// runEditor runs the editor on path with the terminal attached.
func (a *App) runEditor(editor []string, path string) error {
	args := append(append([]string{}, editor[1:]...), path)
	cmd := exec.Command(editor[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %s failed: %w", editor[0], err)
	}
	return nil
}

// ResolveEditor returns the editor command as argv: $EDITOR, else $VISUAL,
// else the first of EditorCandidates found on PATH.
//
// The environment value is split on whitespace, so `EDITOR="code -w"` works;
// an editor path containing spaces does not.
func ResolveEditor() ([]string, error) {
	for _, env := range []string{os.Getenv("EDITOR"), os.Getenv("VISUAL")} {
		fields := strings.Fields(env)
		if len(fields) == 0 {
			continue
		}
		if _, err := exec.LookPath(fields[0]); err == nil {
			return fields, nil
		}
	}
	for _, cand := range EditorCandidates {
		if _, err := exec.LookPath(cand); err == nil {
			return []string{cand}, nil
		}
	}
	return nil, errors.New("no editor found (set $EDITOR)")
}
