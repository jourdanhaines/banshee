package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/session"
)

// fakeRunner records every tmux invocation and hands out synthetic pane ids
// (%0, %1, …) so the builder can be driven without a tmux server.
type fakeRunner struct {
	calls   [][]string
	exists  bool // has-session outcome
	panes   int
	failAt  string // command that should fail
	failIdx int    // occurrence (1-based) of failAt that fails
	seen    map[string]int
}

func newFake() *fakeRunner { return &fakeRunner{seen: map[string]int{}} }

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	f.seen[args[0]]++
	if args[0] == f.failAt && f.seen[args[0]] == f.failIdx {
		return "", errors.New("boom")
	}
	switch args[0] {
	case "has-session":
		if f.exists {
			return "", nil
		}
		return "", errors.New("can't find session")
	case "display-message", "split-window":
		id := fmt.Sprintf("%%%d", f.panes)
		f.panes++
		return id, nil
	}
	return "", nil
}

// argv joins a call for readable diffs.
func argv(call []string) string { return strings.Join(call, " ") }

func mustSession(t *testing.T, jsonSrc string) session.Session {
	t.Helper()
	s, err := session.ParseSession([]byte(jsonSrc), "test.json")
	if err != nil {
		t.Fatalf("fixture did not validate: %v", err)
	}
	return s
}

// testBuilder returns a builder whose filesystem view is limited to dirs.
func testBuilder(r Runner, dirs ...string) *Builder {
	set := map[string]bool{}
	for _, d := range dirs {
		set[d] = true
	}
	return &Builder{
		R:             r,
		Home:          "/home/tester",
		DirExists:     func(p string) bool { return set[p] },
		InTmux:        func() bool { return false },
		AvailableFunc: func() bool { return true },
	}
}

func TestBuildSessionGoldenArgv(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		json    string
		dirs    []string
		defCwd  string
		want    []string
		exists  bool
		wantErr bool
	}{
		{
			name:   "single window single pane",
			target: "solo",
			json:   `{"v":1,"name":"solo","windows":[{"name":"main","panes":[{"run":"nvim"}]}]}`,
			dirs:   []string{"/repo/solo"},
			defCwd: "/repo/solo",
			want: []string{
				"has-session -t =solo",
				"new-session -d -s solo -n main -c /repo/solo",
				"display-message -p -t =solo:{end} #{pane_id}",
				"send-keys -t %0 -l nvim",
				"send-keys -t %0 Enter",
				"select-window -t =solo:^",
			},
		},
		{
			name:   "unnamed window omits -n",
			target: "anon",
			json:   `{"v":1,"name":"anon","windows":[{"panes":[{"run":"top"}]}]}`,
			dirs:   []string{"/repo/anon"},
			defCwd: "/repo/anon",
			want: []string{
				"has-session -t =anon",
				"new-session -d -s anon -c /repo/anon",
				"display-message -p -t =anon:{end} #{pane_id}",
				"send-keys -t %0 -l top",
				"send-keys -t %0 Enter",
				"select-window -t =anon:^",
			},
		},
		{
			name:   "three sibling panes shrink percentages",
			target: "tri",
			json:   `{"v":1,"name":"tri","windows":[{"panes":[{"run":"a"},{"run":"b"},{"run":"c"}]}]}`,
			dirs:   []string{"/repo/tri"},
			defCwd: "/repo/tri",
			want: []string{
				"has-session -t =tri",
				"new-session -d -s tri -c /repo/tri",
				"display-message -p -t =tri:{end} #{pane_id}",
				"split-window -P -F #{pane_id} -h -l 67% -t %0 -c /repo/tri",
				"split-window -P -F #{pane_id} -h -l 50% -t %1 -c /repo/tri",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"send-keys -t %1 -l b",
				"send-keys -t %1 Enter",
				"send-keys -t %2 -l c",
				"send-keys -t %2 Enter",
				"select-window -t =tri:^",
			},
		},
		{
			name:   "nested array splits perpendicular",
			target: "nest",
			json: `{"v":1,"name":"nest","windows":[{"name":"code","panes":[
				{"run":"nvim"},
				[{"run":"lazygit"},{"run":"htop"}]
			]}]}`,
			dirs:   []string{"/repo/nest"},
			defCwd: "/repo/nest",
			want: []string{
				"has-session -t =nest",
				"new-session -d -s nest -n code -c /repo/nest",
				"display-message -p -t =nest:{end} #{pane_id}",
				"split-window -P -F #{pane_id} -h -l 50% -t %0 -c /repo/nest",
				"send-keys -t %0 -l nvim",
				"send-keys -t %0 Enter",
				"split-window -P -F #{pane_id} -v -l 50% -t %1 -c /repo/nest",
				"send-keys -t %1 -l lazygit",
				"send-keys -t %1 Enter",
				"send-keys -t %2 -l htop",
				"send-keys -t %2 Enter",
				"select-window -t =nest:^",
			},
		},
		{
			name:   "doubly nested alternates back to horizontal",
			target: "deep",
			json: `{"v":1,"name":"deep","windows":[{"panes":[
				{"run":"a"},
				[{"run":"b"},[{"run":"c"},{"run":"d"}]]
			]}]}`,
			dirs:   []string{"/w"},
			defCwd: "/w",
			want: []string{
				"has-session -t =deep",
				"new-session -d -s deep -c /w",
				"display-message -p -t =deep:{end} #{pane_id}",
				"split-window -P -F #{pane_id} -h -l 50% -t %0 -c /w",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"split-window -P -F #{pane_id} -v -l 50% -t %1 -c /w",
				"send-keys -t %1 -l b",
				"send-keys -t %1 Enter",
				"split-window -P -F #{pane_id} -h -l 50% -t %2 -c /w",
				"send-keys -t %2 -l c",
				"send-keys -t %2 Enter",
				"send-keys -t %3 -l d",
				"send-keys -t %3 Enter",
				"select-window -t =deep:^",
			},
		},
		{
			name:   "second window uses new-window at end",
			target: "two",
			json: `{"v":1,"name":"two","windows":[
				{"name":"one","panes":[{"run":"a"}]},
				{"name":"two","panes":[{"run":"b"}]}
			]}`,
			dirs:   []string{"/w"},
			defCwd: "/w",
			want: []string{
				"has-session -t =two",
				"new-session -d -s two -n one -c /w",
				"display-message -p -t =two:{end} #{pane_id}",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"new-window -d -t =two: -n two -c /w",
				"display-message -p -t =two:{end} #{pane_id}",
				"send-keys -t %1 -l b",
				"send-keys -t %1 Enter",
				"select-window -t =two:^",
			},
		},
		{
			name:   "session cwd overrides default and window inherits",
			target: "cwd",
			json:   `{"v":1,"name":"cwd","cwd":"/custom","windows":[{"panes":[{"run":"a"}]}]}`,
			dirs:   []string{"/custom", "/repo/cwd"},
			defCwd: "/repo/cwd",
			want: []string{
				"has-session -t =cwd",
				"new-session -d -s cwd -c /custom",
				"display-message -p -t =cwd:{end} #{pane_id}",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"select-window -t =cwd:^",
			},
		},
		{
			name:   "window cwd wins, missing window cwd falls back to session cwd",
			target: "wcwd",
			json: `{"v":1,"name":"wcwd","cwd":"/base","windows":[
				{"panes":[{"run":"a"}],"cwd":"/other"},
				{"panes":[{"run":"b"}],"cwd":"/does/not/exist"}
			]}`,
			dirs:   []string{"/base", "/other"},
			defCwd: "/repo/wcwd",
			want: []string{
				"has-session -t =wcwd",
				"new-session -d -s wcwd -c /other",
				"display-message -p -t =wcwd:{end} #{pane_id}",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"new-window -d -t =wcwd: -c /base",
				"display-message -p -t =wcwd:{end} #{pane_id}",
				"send-keys -t %1 -l b",
				"send-keys -t %1 Enter",
				"select-window -t =wcwd:^",
			},
		},
		{
			name:   "unknown session cwd falls back to home",
			target: "nohome",
			json:   `{"v":1,"name":"nohome","cwd":"/gone","windows":[{"panes":[{"run":"a"}]}]}`,
			dirs:   []string{"/home/tester"},
			defCwd: "/also/gone",
			want: []string{
				"has-session -t =nohome",
				"new-session -d -s nohome -c /home/tester",
				"display-message -p -t =nohome:{end} #{pane_id}",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"select-window -t =nohome:^",
			},
		},
		{
			name:   "pane cwd is typed as cd before the command",
			target: "panecwd",
			json:   `{"v":1,"name":"panecwd","windows":[{"panes":[{"run":"a","cwd":"/sub"},{"cwd":"/gone"}]}]}`,
			dirs:   []string{"/w", "/sub"},
			defCwd: "/w",
			want: []string{
				"has-session -t =panecwd",
				"new-session -d -s panecwd -c /w",
				"display-message -p -t =panecwd:{end} #{pane_id}",
				"split-window -P -F #{pane_id} -h -l 50% -t %0 -c /w",
				"send-keys -t %0 -l cd /sub",
				"send-keys -t %0 Enter",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"select-window -t =panecwd:^",
			},
		},
		{
			name:   "empty pane object issues no keys",
			target: "empty",
			json:   `{"v":1,"name":"empty","windows":[{"panes":[{}]}]}`,
			dirs:   []string{"/w"},
			defCwd: "/w",
			want: []string{
				"has-session -t =empty",
				"new-session -d -s empty -c /w",
				"display-message -p -t =empty:{end} #{pane_id}",
				"select-window -t =empty:^",
			},
		},
		{
			name:   "name sanitization dots and colons",
			target: "dot.repo:x",
			json:   `{"v":1,"name":"ignored","windows":[{"panes":[{"run":"a"}]}]}`,
			dirs:   []string{"/w"},
			defCwd: "/w",
			want: []string{
				"has-session -t =dot_repo_x",
				"new-session -d -s dot_repo_x -c /w",
				"display-message -p -t =dot_repo_x:{end} #{pane_id}",
				"send-keys -t %0 -l a",
				"send-keys -t %0 Enter",
				"select-window -t =dot_repo_x:^",
			},
		},
		{
			name:   "already running session is left alone",
			target: "live",
			json:   `{"v":1,"name":"live","windows":[{"panes":[{"run":"a"}]}]}`,
			dirs:   []string{"/w"},
			defCwd: "/w",
			exists: true,
			want:   []string{"has-session -t =live"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.exists = tc.exists
			b := testBuilder(f, tc.dirs...)
			err := b.BuildSession(tc.target, mustSession(t, tc.json), tc.defCwd)
			if (err != nil) != tc.wantErr {
				t.Fatalf("BuildSession error = %v, wantErr %v", err, tc.wantErr)
			}
			got := make([]string, 0, len(f.calls))
			for _, c := range f.calls {
				got = append(got, argv(c))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("argv mismatch\n got: %s\nwant: %s",
					strings.Join(got, "\n      "), strings.Join(tc.want, "\n      "))
			}
		})
	}
}

func TestBuildSessionSplitFailureAborts(t *testing.T) {
	f := newFake()
	f.failAt = "split-window"
	f.failIdx = 1
	b := testBuilder(f, "/w")
	s := mustSession(t, `{"v":1,"name":"x","windows":[{"panes":[{"run":"a"},{"run":"b"}]}]}`)
	if err := b.BuildSession("x", s, "/w"); err == nil {
		t.Fatal("expected error when split-window fails")
	}
	last := argv(f.calls[len(f.calls)-1])
	if !strings.HasPrefix(last, "split-window") {
		t.Errorf("builder kept going after failure, last call = %q", last)
	}
}

func TestSplitPercentages(t *testing.T) {
	// 100 - 100/(n-i+1), clamped to 1..99 — the v0.3 formula.
	tests := []struct {
		panes int
		want  []string
	}{
		{2, []string{"50%"}},
		{3, []string{"67%", "50%"}},
		{4, []string{"75%", "67%", "50%"}},
		{5, []string{"80%", "75%", "67%", "50%"}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%d panes", tc.panes), func(t *testing.T) {
			panes := make([]string, tc.panes)
			for i := range panes {
				panes[i] = `{"run":"x"}`
			}
			src := fmt.Sprintf(`{"v":1,"name":"p","windows":[{"panes":[%s]}]}`, strings.Join(panes, ","))
			f := newFake()
			b := testBuilder(f, "/w")
			if err := b.BuildSession("p", mustSession(t, src), "/w"); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range f.calls {
				if c[0] == "split-window" {
					got = append(got, c[6]) // -P -F fmt dir -l <pct>
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("percentages = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreatePlainSession(t *testing.T) {
	t.Run("creates when absent", func(t *testing.T) {
		f := newFake()
		b := testBuilder(f)
		if err := b.CreatePlainSession("demo", "/repo/demo"); err != nil {
			t.Fatal(err)
		}
		want := []string{"has-session -t =demo", "new-session -d -s demo -c /repo/demo"}
		got := []string{argv(f.calls[0]), argv(f.calls[1])}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
	t.Run("idempotent when present", func(t *testing.T) {
		f := newFake()
		f.exists = true
		b := testBuilder(f)
		if err := b.CreatePlainSession("demo", "/repo/demo"); err != nil {
			t.Fatal(err)
		}
		if len(f.calls) != 1 {
			t.Errorf("expected only has-session, got %v", f.calls)
		}
	})
}

func TestAttachOrSwitch(t *testing.T) {
	t.Run("outside tmux attaches", func(t *testing.T) {
		f := newFake()
		b := testBuilder(f)
		if err := b.AttachOrSwitch("demo"); err != nil {
			t.Fatal(err)
		}
		if got := argv(f.calls[0]); got != "attach-session -t =demo" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("inside tmux switches client", func(t *testing.T) {
		f := newFake()
		b := testBuilder(f)
		b.InTmux = func() bool { return true }
		if err := b.AttachOrSwitch("demo"); err != nil {
			t.Fatal(err)
		}
		if got := argv(f.calls[0]); got != "switch-client -t =demo" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("AttachFunc overrides outside tmux", func(t *testing.T) {
		f := newFake()
		b := testBuilder(f)
		called := ""
		b.AttachFunc = func(name string) error { called = name; return nil }
		if err := b.AttachOrSwitch("demo"); err != nil {
			t.Fatal(err)
		}
		if called != "demo" || len(f.calls) != 0 {
			t.Errorf("AttachFunc not used: called=%q calls=%v", called, f.calls)
		}
	})
}

func TestSessionName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"repo", "repo"},
		{"my.repo", "my_repo"},
		{"a:b", "a_b"},
		{"/home/me/dev/black.sheep", "black_sheep"},
		{"dot.repo:x", "dot_repo_x"},
	}
	for _, tc := range tests {
		if got := SessionName(tc.in); got != tc.want {
			t.Errorf("SessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDetailSurfacesTmuxStderr covers the diagnostics tmux writes to stderr.
// exec.Cmd.Output captures them into (*exec.ExitError).Stderr, but
// ExitError.Error() renders only "exit status 1" — so wrapping with %w alone
// turned "duplicate session: banshee" into a message nobody can act on.
func TestDetailSurfacesTmuxStderr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil stays nil", nil, ""},
		{"a plain error is untouched", errors.New("boom"), "boom"},
		{
			"stderr is appended",
			&exec.ExitError{ProcessState: &os.ProcessState{}, Stderr: []byte("duplicate session: banshee\n")},
			"duplicate session: banshee",
		},
		{
			"empty stderr adds nothing",
			&exec.ExitError{ProcessState: &os.ProcessState{}, Stderr: []byte("  \n")},
			"exit status",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Detail(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("Detail(nil) = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("Detail must keep the original error unwrappable, got %v", got)
			}
			if !strings.Contains(got.Error(), tt.want) {
				t.Fatalf("Detail(%v) = %q, want it to contain %q", tt.err, got, tt.want)
			}
		})
	}
}
