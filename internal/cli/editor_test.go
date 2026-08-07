package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/session"
)

// fakeEditor writes a shell script that replaces the edited file with body and
// points $EDITOR at it. Nothing but /bin/sh is required.
func fakeEditor(t *testing.T, body string) {
	t.Helper()
	if err := exec.Command("/bin/sh", "-c", "exit 0").Run(); err != nil {
		t.Skipf("cannot run /bin/sh here: %v", err)
	}
	script := filepath.Join(t.TempDir(), "editor")
	content := "#!/bin/sh\ncat > \"$1\" <<'BANSHEE_EOF'\n" + body + "\nBANSHEE_EOF\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script)
	t.Setenv("VISUAL", "")
}

func TestEditSessionValidConfig(t *testing.T) {
	fakeEditor(t, `{"v":1,"name":"demo","windows":[{"panes":[{"run":"nvim"}]}]}`)
	ta := newTestApp(t)

	if err := ta.EditSession("demo", false); err != nil {
		t.Fatalf("EditSession: %v", err)
	}
	s, err := session.LoadSession(session.SessionPath(ta.Res.SessionsDir, "demo"))
	if err != nil {
		t.Fatalf("saved config does not validate: %v", err)
	}
	if s.Name != "demo" {
		t.Errorf("name = %q", s.Name)
	}
	// no_load mode must not touch tmux
	if len(ta.runner.calls) != 0 {
		t.Errorf("editing must not build a session: %v", ta.runner.calls)
	}
}

func TestEditSessionInvalidThenCancelRemovesNewFile(t *testing.T) {
	fakeEditor(t, `not json at all`)
	ta := newTestApp(t)
	ta.In = strings.NewReader("c\n")

	err := ta.EditSession("demo", false)
	if err == nil {
		t.Fatal("expected the cancel to surface as an error")
	}
	if _, statErr := os.Stat(session.SessionPath(ta.Res.SessionsDir, "demo")); !os.IsNotExist(statErr) {
		t.Error("a config created by this call should be removed on cancel")
	}
	if !strings.Contains(ta.err.String(), "config invalid") {
		t.Errorf("stderr = %q", ta.err.String())
	}
}

func TestEditSessionCancelKeepsPreexistingFile(t *testing.T) {
	fakeEditor(t, `nope`)
	ta := newTestApp(t)
	ta.In = strings.NewReader("c\n")
	ta.writeSession(t, "demo", `{"v":1,"name":"demo","windows":[{"panes":[{"run":"a"}]}]}`)

	if err := ta.EditSession("demo", false); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := os.Stat(session.SessionPath(ta.Res.SessionsDir, "demo")); err != nil {
		t.Error("a pre-existing config must survive a cancel")
	}
}

func TestEditSessionLoadsAfterSave(t *testing.T) {
	fakeEditor(t, `{"v":1,"name":"demo","windows":[{"panes":[{"run":"nvim"}]}]}`)
	ta := newTestApp(t)

	if err := ta.EditSession("demo", true); err != nil {
		t.Fatalf("EditSession: %v", err)
	}
	if len(ta.runner.calls) == 0 {
		t.Fatal("load mode should have built the session")
	}
	if got := strings.Join(ta.runner.calls[len(ta.runner.calls)-1], " "); got != "attach-session -t =demo" {
		t.Errorf("last call = %q", got)
	}
}

func TestEditSessionRequireConfigFlow(t *testing.T) {
	fakeEditor(t, `{"v":1,"name":"fresh","windows":[{"panes":[{"run":"a"}]}]}`)
	ta := newTestApp(t)
	ta.Res.EditSession = func(target string) error { return ta.EditSession(target, true) }

	if code := ta.Run([]string{"-s", "fresh"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	if _, err := session.LoadSession(session.SessionPath(ta.Res.SessionsDir, "fresh")); err != nil {
		t.Errorf("config not written: %v", err)
	}
	last, err := ta.State.Read()
	if err != nil || last.String() != "target:fresh" {
		t.Errorf("last action = %v (%v)", last, err)
	}
}

func TestResolveEditor(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	t.Run("EDITOR wins", func(t *testing.T) {
		t.Setenv("EDITOR", "sh")
		t.Setenv("VISUAL", "cat")
		got, err := ResolveEditor()
		if err != nil || got[0] != "sh" {
			t.Errorf("ResolveEditor = %v, %v", got, err)
		}
	})
	t.Run("VISUAL is the fallback", func(t *testing.T) {
		t.Setenv("EDITOR", "")
		t.Setenv("VISUAL", "sh")
		got, err := ResolveEditor()
		if err != nil || got[0] != "sh" {
			t.Errorf("ResolveEditor = %v, %v", got, err)
		}
	})
	t.Run("arguments are kept", func(t *testing.T) {
		t.Setenv("EDITOR", "sh -c")
		got, err := ResolveEditor()
		if err != nil || strings.Join(got, " ") != "sh -c" {
			t.Errorf("ResolveEditor = %v, %v", got, err)
		}
	})
	t.Run("missing editor falls through to the probe list", func(t *testing.T) {
		t.Setenv("EDITOR", "definitely-not-installed-xyz")
		t.Setenv("VISUAL", "")
		got, err := ResolveEditor()
		if err != nil {
			// No candidate editor installed in this environment.
			t.Skip("no candidate editor available")
		}
		found := false
		for _, c := range EditorCandidates {
			if got[0] == c {
				found = true
			}
		}
		if !found {
			t.Errorf("ResolveEditor = %v, want one of %v", got, EditorCandidates)
		}
	})
}
