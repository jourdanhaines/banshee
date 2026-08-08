package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/session"
	"github.com/jourdanhaines/banshee/internal/state"
	"github.com/jourdanhaines/banshee/internal/tmux"
)

// fakeRunner answers has-session from a set of live session names and records
// every call.
type fakeRunner struct {
	live  map[string]bool
	calls [][]string
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if args[0] == "has-session" {
		if f.live[strings.TrimPrefix(args[2], "=")] {
			return "", nil
		}
		return "", errors.New("no session")
	}
	if args[0] == "display-message" || args[0] == "split-window" {
		return "%0", nil
	}
	return "", nil
}

type fakeIndex struct {
	repos   []index.Repo
	cleared bool
}

func (f *fakeIndex) Repos() []index.Repo { return f.repos }
func (f *fakeIndex) Exact(name string) (index.Repo, bool) {
	var found index.Repo
	n := 0
	for _, r := range f.repos {
		if r.Name == name {
			found, n = r, n+1
		}
	}
	return found, n == 1
}
func (f *fakeIndex) Refresh() error { return nil }
func (f *fakeIndex) Clear() error   { f.cleared = true; return nil }

type testApp struct {
	*App
	out    *bytes.Buffer
	err    *bytes.Buffer
	runner *fakeRunner
	idx    *fakeIndex
	dir    string
}

func newTestApp(t *testing.T, repos ...index.Repo) *testApp {
	t.Helper()
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	groups := filepath.Join(dir, "groups")
	for _, d := range []string{sessions, groups} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	runner := &fakeRunner{live: map[string]bool{}}
	idx := &fakeIndex{repos: repos}
	out, errBuf := &bytes.Buffer{}, &bytes.Buffer{}

	app := &App{
		Cfg:         config.Default(),
		Index:       idx,
		State:       &state.Store{Path: filepath.Join(dir, "last_action")},
		In:          strings.NewReader(""),
		Out:         out,
		Err:         errBuf,
		Interactive: func() bool { return false },
	}
	app.Builder = &tmux.Builder{
		R:             runner,
		Home:          dir,
		InTmux:        func() bool { return false },
		AvailableFunc: func() bool { return true },
		DirExists:     func(string) bool { return true },
	}
	app.Res = &session.Resolver{
		SessionsDir: sessions,
		GroupsDir:   groups,
		Index:       idx,
		Builder:     app.Builder,
		Recorder:    app.State,
		Home:        dir,
	}
	return &testApp{App: app, out: out, err: errBuf, runner: runner, idx: idx, dir: dir}
}

func (ta *testApp) writeSession(t *testing.T, target, body string) {
	t.Helper()
	if err := os.WriteFile(session.SessionPath(ta.Res.SessionsDir, target), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (ta *testApp) writeGroup(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(session.GroupPath(ta.Res.GroupsDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunFlagParity(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "help", args: []string{"-h"}, wantOut: "Usage:"},
		{name: "long help", args: []string{"--help"}, wantOut: "Usage:"},
		{name: "version", args: []string{"-v"}, wantOut: "banshee " + config.Version},
		{name: "long version", args: []string{"--version"}, wantOut: "banshee " + config.Version},
		{name: "unknown option", args: []string{"-x"}, wantCode: 1, wantErr: "unknown option '-x'"},
		{name: "unknown long option", args: []string{"--nope"}, wantCode: 1, wantErr: "unknown option '--nope'"},
		{name: "-s without target", args: []string{"-s"}, wantCode: 1, wantErr: "-s requires a target name"},
		{name: "-se without target", args: []string{"-se"}, wantCode: 1, wantErr: "-se requires a target name"},
		{name: "-g without name", args: []string{"-g"}, wantCode: 1, wantErr: "-g requires a group name"},
		{name: "-ge without name", args: []string{"-ge"}, wantCode: 1, wantErr: "-ge requires a group name"},
		{name: "-r with no last action", args: []string{"-r"}, wantCode: 1, wantErr: "no previous action"},
		{name: "empty list", args: []string{"-l"}, wantOut: "no session configs or groups"},
		{name: "clear cache", args: []string{"-c"}, wantOut: "cache cleared"},
		{name: "unknown complete kind", args: []string{"_complete", "wat"}, wantCode: 1, wantErr: "unknown kind"},
		{name: "daemon verb without hooks", args: []string{"toggle"}, wantCode: 1, wantErr: "not available in this build"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ta := newTestApp(t)
			code := ta.Run(tc.args)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, ta.err.String())
			}
			if tc.wantOut != "" && !strings.Contains(ta.out.String(), tc.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", ta.out.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(ta.err.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", ta.err.String(), tc.wantErr)
			}
		})
	}
}

func TestUnknownOptionPrintsUsageToStderr(t *testing.T) {
	ta := newTestApp(t)
	ta.Run([]string{"-z"})
	if !strings.Contains(ta.err.String(), "Usage:") {
		t.Errorf("usage should go to stderr, got %q", ta.err.String())
	}
	if ta.out.Len() != 0 {
		t.Errorf("stdout should stay empty, got %q", ta.out.String())
	}
}

func TestClearDelegatesToIndex(t *testing.T) {
	ta := newTestApp(t)
	if code := ta.Run([]string{"-c"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !ta.idx.cleared {
		t.Error("Index.Clear was not called")
	}
}

func TestListOutput(t *testing.T) {
	ta := newTestApp(t)
	ta.writeSession(t, "alpha", `{"v":1,"name":"alpha","windows":[{"panes":[{"run":"a"}]}]}`)
	ta.writeSession(t, "beta", `{"v":1,"name":"beta","windows":[{"panes":[{"run":"a"}]}]}`)
	ta.writeGroup(t, "work", `{"v":1,"name":"work","targets":["alpha","beta"]}`)
	ta.writeGroup(t, "broken", `{"v":1,"name":"broken"}`)
	ta.runner.live["alpha"] = true
	if err := ta.State.Record(state.KindGroup, "work"); err != nil {
		t.Fatal(err)
	}

	if code := ta.Run([]string{"-l"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	got := ta.out.String()
	for _, want := range []string{
		"Targets:",
		"  alpha                    [running]\n",
		"  beta                     [stopped]\n",
		"Groups:",
		"  broken\n",
		"    (invalid group config)\n",
		"  work (last)\n",
		"    alpha                  [running]\n",
		"    beta                   [stopped]\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("list output missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestCompleteSubcommand(t *testing.T) {
	ta := newTestApp(t, index.Repo{Name: "beta", Path: "/r/beta"}, index.Repo{Name: "alpha", Path: "/r/alpha"})
	ta.writeSession(t, "zeta", `{"v":1,"name":"zeta","windows":[{"panes":[{"run":"a"}]}]}`)
	ta.writeGroup(t, "work", `{"v":1,"name":"work","targets":["zeta"]}`)

	tests := []struct {
		kind string
		want []string
	}{
		{"repos", []string{"alpha", "beta"}},
		{"targets", []string{"zeta"}},
		{"groups", []string{"work"}},
		{"pool", []string{"alpha", "beta", "zeta"}},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			ta.out.Reset()
			if code := ta.Run([]string{"_complete", tc.kind}); code != 0 {
				t.Fatalf("exit %d: %s", code, ta.err.String())
			}
			got := strings.Fields(ta.out.String())
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("_complete %s = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

func TestOpenExactTargetConfig(t *testing.T) {
	ta := newTestApp(t)
	ta.writeSession(t, "demo", `{"v":1,"name":"demo","windows":[{"panes":[{"run":"nvim"}]}]}`)

	if code := ta.Run([]string{"demo"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	var got []string
	for _, c := range ta.runner.calls {
		got = append(got, strings.Join(c, " "))
	}
	want := []string{
		"has-session -t =demo",
		"new-session -d -s demo -c " + ta.dir,
		"display-message -p -t =demo:{end} #{pane_id}",
		"send-keys -t %0 -l nvim",
		"send-keys -t %0 Enter",
		"select-window -t =demo:^",
		"attach-session -t =demo",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("argv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	last, err := ta.State.Read()
	if err != nil || last.String() != "target:demo" {
		t.Errorf("last action = %v (%v)", last, err)
	}
}

func TestOpenExactRepoWithoutConfig(t *testing.T) {
	ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
	if code := ta.Run([]string{"demo"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	if got := strings.Join(ta.runner.calls[1], " "); got != "new-session -d -s demo -c /repo/demo" {
		t.Errorf("second call = %q", got)
	}
}

func TestRestoreReplaysLastAction(t *testing.T) {
	ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
	if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
		t.Fatal(err)
	}
	if code := ta.Run([]string{"-r"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	if got := strings.Join(ta.runner.calls[len(ta.runner.calls)-1], " "); got != "attach-session -t =demo" {
		t.Errorf("last call = %q", got)
	}
}

func TestRestoreMalformedLastAction(t *testing.T) {
	ta := newTestApp(t)
	if err := os.WriteFile(ta.State.Path, []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := ta.Run([]string{"-r"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(ta.err.String(), "malformed last_action") {
		t.Errorf("stderr = %q", ta.err.String())
	}
}

func TestDaemonHooks(t *testing.T) {
	ta := newTestApp(t)
	var calls []string
	ta.Hooks = Hooks{
		Daemon: func() error { calls = append(calls, "daemon"); return nil },
		Toggle: func(q string) error { calls = append(calls, "toggle:"+q); return nil },
		Show:   func(q string) error { calls = append(calls, "show:"+q); return nil },
		Hide:   func() error { calls = append(calls, "hide"); return nil },
		Reload: func() error { calls = append(calls, "reload"); return nil },
		Quit:   func() error { calls = append(calls, "quit"); return nil },
	}
	for _, args := range [][]string{
		{"daemon"}, {"toggle"}, {"toggle", "black", "sheep"}, {"show", "q"}, {"hide"}, {"reload"}, {"quit"},
	} {
		if code := ta.Run(args); code != 0 {
			t.Fatalf("%v: exit %d (%s)", args, code, ta.err.String())
		}
	}
	want := []string{"daemon", "toggle:", "toggle:black sheep", "show:q", "hide", "reload", "quit"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("hook calls = %v, want %v", calls, want)
	}
}

func TestOpenRunningSession(t *testing.T) {
	// A running tmux session that is neither a config target nor a repo
	// (e.g. bare `tmux new` → "0") must attach directly, not open the picker.
	ta := newTestApp(t)
	ta.runner.live["0"] = true
	if code := ta.Run([]string{"0"}); code != 0 {
		t.Fatalf("exit %d: %s", code, ta.err.String())
	}
	if got := strings.Join(ta.runner.calls[len(ta.runner.calls)-1], " "); got != "attach-session -t =0" {
		t.Errorf("last call = %q", got)
	}
	for _, c := range ta.runner.calls {
		if c[0] == "new-session" {
			t.Errorf("must not create a session: %v", c)
		}
	}
}

func TestStartupPrompt(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("BANSHEE_STARTUP_CHECKED", "")

	t.Run("silent when not interactive", func(t *testing.T) {
		ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
			t.Fatal(err)
		}
		if code := ta.Run([]string{"_startup-prompt"}); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if ta.err.Len() != 0 {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("silent when the session is already running", func(t *testing.T) {
		ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		ta.Interactive = func() bool { return true }
		ta.runner.live["demo"] = true
		if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
			t.Fatal(err)
		}
		ta.Run([]string{"_startup-prompt"})
		if ta.err.Len() != 0 {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("silent when disabled in config", func(t *testing.T) {
		ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		ta.Interactive = func() bool { return true }
		ta.Cfg.StartupPrompt = false
		if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
			t.Fatal(err)
		}
		ta.Run([]string{"_startup-prompt"})
		if ta.err.Len() != 0 {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("prompts and restores on enter", func(t *testing.T) {
		ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		ta.Interactive = func() bool { return true }
		ta.In = strings.NewReader("\n")
		if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
			t.Fatal(err)
		}
		if code := ta.Run([]string{"_startup-prompt"}); code != 0 {
			t.Fatalf("exit %d: %s", code, ta.err.String())
		}
		if !strings.Contains(ta.err.String(), "restore last target 'demo'? [Y/n]") {
			t.Errorf("stderr = %q", ta.err.String())
		}
		if got := strings.Join(ta.runner.calls[len(ta.runner.calls)-1], " "); got != "attach-session -t =demo" {
			t.Errorf("last call = %q", got)
		}
	})

	t.Run("declining does nothing", func(t *testing.T) {
		ta := newTestApp(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		ta.Interactive = func() bool { return true }
		ta.In = strings.NewReader("n\n")
		if err := ta.State.Record(state.KindTarget, "demo"); err != nil {
			t.Fatal(err)
		}
		ta.Run([]string{"_startup-prompt"})
		for _, c := range ta.runner.calls {
			if c[0] == "new-session" || c[0] == "attach-session" {
				t.Errorf("declined prompt still acted: %v", c)
			}
		}
	})

	t.Run("group with one stopped target prompts", func(t *testing.T) {
		ta := newTestApp(t)
		ta.Interactive = func() bool { return true }
		ta.In = strings.NewReader("n\n")
		ta.writeGroup(t, "work", `{"v":1,"name":"work","targets":["one","two"]}`)
		ta.runner.live["one"] = true
		if err := ta.State.Record(state.KindGroup, "work"); err != nil {
			t.Fatal(err)
		}
		ta.Run([]string{"_startup-prompt"})
		if !strings.Contains(ta.err.String(), "restore last group 'work'? [Y/n]") {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})

	t.Run("group fully running stays silent", func(t *testing.T) {
		ta := newTestApp(t)
		ta.Interactive = func() bool { return true }
		ta.writeGroup(t, "work", `{"v":1,"name":"work","targets":["one","two"]}`)
		ta.runner.live["one"] = true
		ta.runner.live["two"] = true
		if err := ta.State.Record(state.KindGroup, "work"); err != nil {
			t.Fatal(err)
		}
		ta.Run([]string{"_startup-prompt"})
		if ta.err.Len() != 0 {
			t.Errorf("stderr = %q", ta.err.String())
		}
	})
}

func TestFloatFirst(t *testing.T) {
	pool := []string{"alpha", "beta", "gamma"}
	tests := []struct {
		name  string
		first []string
		want  []string
	}{
		{name: "no current keeps order", want: pool},
		{name: "current floats", first: []string{"gamma"}, want: []string{"gamma", "alpha", "beta"}},
		{name: "unknown entries are dropped", first: []string{"zzz", "beta"}, want: []string{"beta", "alpha", "gamma"}},
		{name: "duplicates collapse", first: []string{"beta", "beta"}, want: []string{"beta", "alpha", "gamma"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := floatFirst(pool, tc.first)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("floatFirst = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSplitOpts(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"--color=16", []string{"--color=16"}},
		{"  --color=16   --height=40%  ", []string{"--color=16", "--height=40%"}},
	}
	for _, tc := range tests {
		got := splitOpts(tc.in)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("splitOpts(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/me/dev/repo", "repo"},
		{"/home/me/dev/repo/", "repo"},
		{"repo", "repo"},
	}
	for _, tc := range tests {
		if got := baseName(tc.in); got != tc.want {
			t.Errorf("baseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCancelledPickerIsSilent covers the primary Ctrl-F flow. Esc is how the
// picker is normally dismissed, and the zsh/bash widget runs `banshee` and then
// redraws the prompt — so an error line printed on cancel landed above the
// prompt every single time. v0.3 was silent here on purpose.
func TestCancelledPickerIsSilent(t *testing.T) {
	repos := []index.Repo{
		{Name: "alpha", Path: "/dev/alpha"},
		{Name: "beta", Path: "/dev/beta"},
	}

	tests := []struct {
		name string
		args []string
	}{
		{"bare invocation", nil},
		{"query with no exact match", []string{"nomatch"}},
		{"_pick-repo for the no-tmux shell fallback", []string{"_pick-repo", "nomatch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta := newTestApp(t, repos...)
			// No fzf, and stdin is at EOF: the numbered picker reads nothing,
			// which is the non-interactive stand-in for pressing Esc.
			ta.HasFzf = func() bool { return false }

			if code := ta.Run(tt.args); code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr: %q)", code, ta.err.String())
			}
			if strings.Contains(ta.err.String(), "cancelled") {
				t.Fatalf("stderr = %q, want no cancellation message", ta.err.String())
			}
			if strings.TrimSpace(ta.out.String()) != "" {
				t.Fatalf("stdout = %q, want nothing", ta.out.String())
			}
		})
	}
}

// TestPickRepoPrintsPath covers the hidden verb the shell plugins call when
// tmux is missing: a binary cannot cd its parent, so the shell asks banshee
// which repo was picked and cds there itself.
func TestPickRepoPrintsPath(t *testing.T) {
	ta := newTestApp(t, index.Repo{Name: "alpha", Path: "/dev/alpha"})
	ta.HasFzf = func() bool { return false }

	if code := ta.Run([]string{"_pick-repo", "alpha"}); code != 0 {
		t.Fatalf("exit code = %d (stderr: %q)", code, ta.err.String())
	}
	if got := strings.TrimSpace(ta.out.String()); got != "/dev/alpha" {
		t.Fatalf("stdout = %q, want the repo path", got)
	}
	// No tmux session was built: _pick-repo only resolves a path.
	if len(ta.runner.calls) != 0 {
		t.Fatalf("tmux was invoked: %v", ta.runner.calls)
	}
}
