package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers/connectors"
)

func newLinkRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestLink(t *testing.T) {
	t.Run("positional binding writes without prompting", func(t *testing.T) {
		ta := newTestApp(t)
		repo := newLinkRepo(t)
		if code := ta.Run([]string{"link", "railway", repo, "proj-1"}); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, ta.err)
		}
		rc, err := connectors.LoadRepoConfig(repo)
		if err != nil {
			t.Fatal(err)
		}
		if rc.Connectors["railway"] != "proj-1" {
			t.Errorf("binding = %q, want proj-1", rc.Connectors["railway"])
		}
		if !strings.Contains(ta.out.String(), "linked railway in "+repo) {
			t.Errorf("stdout = %q, want success line", ta.out.String())
		}
	})

	t.Run("subdirectory path resolves to the repo root", func(t *testing.T) {
		ta := newTestApp(t)
		repo := newLinkRepo(t)
		sub := filepath.Join(repo, "a", "b")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if code := ta.Run([]string{"link", "railway", sub, "proj-2"}); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, ta.err)
		}
		rc, err := connectors.LoadRepoConfig(repo)
		if err != nil {
			t.Fatal(err)
		}
		if rc.Connectors["railway"] != "proj-2" {
			t.Errorf("binding = %q, want proj-2 at repo root", rc.Connectors["railway"])
		}
	})

	t.Run("piped stdin answers the prompt", func(t *testing.T) {
		ta := newTestApp(t)
		ta.In = strings.NewReader("https://railway.com/project/abc\n")
		repo := newLinkRepo(t)
		if code := ta.Run([]string{"link", "railway", repo}); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, ta.err)
		}
		rc, err := connectors.LoadRepoConfig(repo)
		if err != nil {
			t.Fatal(err)
		}
		if rc.Connectors["railway"] != "https://railway.com/project/abc" {
			t.Errorf("binding = %q", rc.Connectors["railway"])
		}
		if !strings.Contains(ta.err.String(), "Railway project URL or ID:") {
			t.Errorf("prompt = %q, want the builtin's display name", ta.err.String())
		}
	})

	t.Run("missing id fails", func(t *testing.T) {
		ta := newTestApp(t)
		if code := ta.Run([]string{"link"}); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(ta.err.String(), "requires a connector id") {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("malformed id fails", func(t *testing.T) {
		ta := newTestApp(t)
		if code := ta.Run([]string{"link", "-bad id-", t.TempDir(), "x"}); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
	})

	t.Run("path outside a repo fails", func(t *testing.T) {
		ta := newTestApp(t)
		if code := ta.Run([]string{"link", "railway", t.TempDir(), "x"}); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(ta.err.String(), "not inside a git repository") {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("empty prompt reply cancels", func(t *testing.T) {
		ta := newTestApp(t)
		ta.In = strings.NewReader("\n")
		repo := newLinkRepo(t)
		if code := ta.Run([]string{"link", "railway", repo}); code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(ta.err.String(), "cancelled") {
			t.Errorf("stderr = %q, want cancelled", ta.err.String())
		}
	})

	t.Run("usage documents the verb", func(t *testing.T) {
		ta := newTestApp(t)
		ta.Run([]string{"-h"})
		if !strings.Contains(ta.out.String(), "banshee link <id> [path] [binding]") {
			t.Error("usage text missing link verb")
		}
	})
}
