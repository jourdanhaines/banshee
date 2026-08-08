package connectors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeRunner counts calls and returns canned output, satisfying tmux.Runner.
type fakeRunner struct {
	out   string
	err   error
	calls int
	argv  []string
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls++
	f.argv = args
	return f.out, f.err
}

func TestPickActivePanePath(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want string
		ok   bool
	}{
		{
			name: "single client",
			out:  "1700000000\t/home/u/dev/repo",
			want: "/home/u/dev/repo",
			ok:   true,
		},
		{
			name: "most recently active wins",
			out:  "100\t/old\n300\t/newest\n200\t/mid",
			want: "/newest",
			ok:   true,
		},
		{
			name: "malformed lines skipped",
			out:  "nonsense\nx\ty\tz\n150\t/good",
			want: "/good",
			ok:   true,
		},
		{
			name: "relative path skipped",
			out:  "100\trelative/path",
			ok:   false,
		},
		{
			name: "empty output",
			out:  "",
			ok:   false,
		},
		{
			name: "blank path skipped",
			out:  "100\t",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := pickActivePanePath(tt.out)
			if ok != tt.ok || got != tt.want {
				t.Errorf("pickActivePanePath = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFindGitRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "wt")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree has a .git *file*, not a directory.
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		dir  string
		want string
		ok   bool
	}{
		{name: "repo root itself", dir: repo, want: repo, ok: true},
		{name: "deep subdirectory walks up", dir: deep, want: repo, ok: true},
		{name: "worktree .git file", dir: worktree, want: worktree, ok: true},
		{name: "outside any repo", dir: root, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FindGitRoot(tt.dir)
			if ok != tt.ok || got != tt.want {
				t.Errorf("FindGitRoot(%q) = (%q, %v), want (%q, %v)", tt.dir, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestTmuxCurrentRepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	t.Run("resolves repo and golden argv", func(t *testing.T) {
		r := &fakeRunner{out: "100\t" + repo}
		fn := TmuxCurrentRepo(r)
		root, name, ok := fn(ctx)
		if !ok || root != repo || name != filepath.Base(repo) {
			t.Fatalf("got (%q, %q, %v), want (%q, %q, true)", root, name, ok, repo, filepath.Base(repo))
		}
		wantArgv := []string{"list-clients", "-F", "#{client_activity}\t#{pane_current_path}"}
		if len(r.argv) != len(wantArgv) {
			t.Fatalf("argv = %v, want %v", r.argv, wantArgv)
		}
		for i := range wantArgv {
			if r.argv[i] != wantArgv[i] {
				t.Fatalf("argv = %v, want %v", r.argv, wantArgv)
			}
		}
	})

	t.Run("runner error degrades silently", func(t *testing.T) {
		fn := TmuxCurrentRepo(&fakeRunner{err: errors.New("no server")})
		if _, _, ok := fn(ctx); ok {
			t.Error("want ok=false on runner error")
		}
	})

	t.Run("cwd outside a repo degrades", func(t *testing.T) {
		fn := TmuxCurrentRepo(&fakeRunner{out: "100\t" + t.TempDir()})
		if _, _, ok := fn(ctx); ok {
			t.Error("want ok=false outside a git repo")
		}
	})

	t.Run("TTL caches including negatives", func(t *testing.T) {
		r := &fakeRunner{err: errors.New("no server")}
		fn := TmuxCurrentRepo(r)
		fn(ctx)
		fn(ctx)
		fn(ctx)
		if r.calls != 1 {
			t.Errorf("runner calls = %d, want 1 within TTL", r.calls)
		}
	})

	t.Run("cancelled ctx short-circuits", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		r := &fakeRunner{out: "100\t" + repo}
		fn := TmuxCurrentRepo(r)
		if _, _, ok := fn(cancelled); ok {
			t.Error("want ok=false with cancelled ctx")
		}
		if r.calls != 0 {
			t.Errorf("runner calls = %d, want 0 with cancelled ctx", r.calls)
		}
	})
}
