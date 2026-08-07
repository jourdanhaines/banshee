package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/index"
)

// fakeBuilder records what the resolver asked tmux to do.
type fakeBuilder struct {
	available bool
	log       []string
	buildErr  error
	built     map[string]Session
}

func newFakeBuilder() *fakeBuilder {
	return &fakeBuilder{available: true, built: map[string]Session{}}
}

func (f *fakeBuilder) Available() bool { return f.available }
func (f *fakeBuilder) SessionName(target string) string {
	return strings.NewReplacer(".", "_", ":", "_").Replace(filepath.Base(target))
}

func (f *fakeBuilder) BuildSession(target string, s Session, defaultCwd string) error {
	f.log = append(f.log, "build "+target+" cwd="+defaultCwd)
	if f.buildErr != nil {
		return f.buildErr
	}
	f.built[target] = s
	return nil
}

func (f *fakeBuilder) CreatePlainSession(name, cwd string) error {
	f.log = append(f.log, "plain "+name+" cwd="+cwd)
	return nil
}

func (f *fakeBuilder) AttachOrSwitch(name string) error {
	f.log = append(f.log, "attach "+name)
	return nil
}

// fakeIndex is a static repo index.
type fakeIndex struct{ repos []index.Repo }

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
func (f *fakeIndex) Clear() error   { return nil }

// fakeRecorder captures last-action writes.
type fakeRecorder struct{ entries []string }

func (f *fakeRecorder) Record(kind, name string) error {
	f.entries = append(f.entries, kind+":"+name)
	return nil
}

type harness struct {
	res      *Resolver
	builder  *fakeBuilder
	recorder *fakeRecorder
	dir      string
}

func newHarness(t *testing.T, repos ...index.Repo) *harness {
	t.Helper()
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	groups := filepath.Join(dir, "groups")
	for _, d := range []string{sessions, groups} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b := newFakeBuilder()
	rec := &fakeRecorder{}
	return &harness{
		builder:  b,
		recorder: rec,
		dir:      dir,
		res: &Resolver{
			SessionsDir: sessions,
			GroupsDir:   groups,
			Index:       &fakeIndex{repos: repos},
			Builder:     b,
			Recorder:    rec,
			Home:        "/home/tester",
		},
	}
}

func (h *harness) writeSession(t *testing.T, target, body string) {
	t.Helper()
	if err := os.WriteFile(SessionPath(h.res.SessionsDir, target), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) writeGroup(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(GroupPath(h.res.GroupsDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	t.Run("config present builds and attaches", func(t *testing.T) {
		h := newHarness(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		h.writeSession(t, "demo", `{"v":1,"name":"demo","windows":[{"panes":[{"run":"a"}]}]}`)
		if err := h.res.Resolve("demo", ModeDefault, true); err != nil {
			t.Fatal(err)
		}
		want := []string{"build demo cwd=/repo/demo", "attach demo"}
		if !reflect.DeepEqual(h.builder.log, want) {
			t.Errorf("log = %v, want %v", h.builder.log, want)
		}
		if !reflect.DeepEqual(h.recorder.entries, []string{"target:demo"}) {
			t.Errorf("recorded %v", h.recorder.entries)
		}
	})

	t.Run("config without repo falls back to home", func(t *testing.T) {
		h := newHarness(t)
		h.writeSession(t, "solo", `{"v":1,"name":"solo","windows":[{"panes":[{"run":"a"}]}]}`)
		if err := h.res.Resolve("solo", ModeDefault, false); err != nil {
			t.Fatal(err)
		}
		if h.builder.log[0] != "build solo cwd=/home/tester" {
			t.Errorf("log = %v", h.builder.log)
		}
	})

	t.Run("no attach skips attach", func(t *testing.T) {
		h := newHarness(t, index.Repo{Name: "demo", Path: "/repo/demo"})
		if err := h.res.Resolve("demo", ModeDefault, false); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(h.builder.log, []string{"plain demo cwd=/repo/demo"}) {
			t.Errorf("log = %v", h.builder.log)
		}
	})

	t.Run("no config no repo is an error", func(t *testing.T) {
		h := newHarness(t)
		err := h.res.Resolve("ghost", ModeDefault, true)
		if err == nil || !strings.Contains(err.Error(), "no config or matching repo") {
			t.Fatalf("err = %v", err)
		}
		if len(h.recorder.entries) != 0 {
			t.Errorf("failed resolve must not record: %v", h.recorder.entries)
		}
	})

	t.Run("invalid config surfaces the validation error", func(t *testing.T) {
		h := newHarness(t)
		h.writeSession(t, "bad", `{"v":1,"name":"bad"}`)
		err := h.res.Resolve("bad", ModeDefault, true)
		if err == nil || !strings.Contains(err.Error(), `"windows"`) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("require config runs the editor flow", func(t *testing.T) {
		h := newHarness(t)
		edited := ""
		h.res.EditSession = func(target string) error { edited = target; return nil }
		if err := h.res.Resolve("fresh", ModeRequireConfig, true); err != nil {
			t.Fatal(err)
		}
		if edited != "fresh" {
			t.Errorf("EditSession got %q", edited)
		}
		if len(h.builder.log) != 0 {
			t.Errorf("editor flow must not build directly: %v", h.builder.log)
		}
		if len(h.recorder.entries) != 0 {
			t.Errorf("editor flow records via its own load: %v", h.recorder.entries)
		}
	})

	t.Run("require config with existing config builds normally", func(t *testing.T) {
		h := newHarness(t)
		h.writeSession(t, "demo", `{"v":1,"name":"demo","windows":[{"panes":[{"run":"a"}]}]}`)
		h.res.EditSession = func(string) error { return errors.New("should not run") }
		if err := h.res.Resolve("demo", ModeRequireConfig, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("empty target", func(t *testing.T) {
		h := newHarness(t)
		if err := h.res.Resolve("", ModeDefault, true); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("tmux missing", func(t *testing.T) {
		h := newHarness(t)
		h.builder.available = false
		err := h.res.Resolve("demo", ModeDefault, true)
		if err == nil || !strings.Contains(err.Error(), "tmux is not installed") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestResolveGroup(t *testing.T) {
	t.Run("loads every target and attaches to the first", func(t *testing.T) {
		h := newHarness(t,
			index.Repo{Name: "one", Path: "/repo/one"},
			index.Repo{Name: "two", Path: "/repo/two"},
		)
		h.writeGroup(t, "work", `{"v":1,"name":"work","targets":["one","two"]}`)
		if err := h.res.ResolveGroup("work", true); err != nil {
			t.Fatal(err)
		}
		want := []string{"plain one cwd=/repo/one", "plain two cwd=/repo/two", "attach one"}
		if !reflect.DeepEqual(h.builder.log, want) {
			t.Errorf("log = %v, want %v", h.builder.log, want)
		}
		if !reflect.DeepEqual(h.recorder.entries, []string{"target:one", "target:two", "group:work"}) {
			t.Errorf("recorded %v", h.recorder.entries)
		}
	})

	t.Run("a failing target is logged, not fatal", func(t *testing.T) {
		h := newHarness(t, index.Repo{Name: "two", Path: "/repo/two"})
		var logged []string
		h.res.Log = func(format string, args ...any) { logged = append(logged, format) }
		h.writeGroup(t, "work", `{"v":1,"name":"work","targets":["ghost","two"]}`)
		if err := h.res.ResolveGroup("work", true); err != nil {
			t.Fatal(err)
		}
		if len(logged) != 1 {
			t.Errorf("expected one logged failure, got %v", logged)
		}
		// Attaches to the first listed target even though it failed — parity
		// with v0.3.
		if got := h.builder.log[len(h.builder.log)-1]; got != "attach ghost" {
			t.Errorf("last call = %q", got)
		}
	})

	t.Run("missing group config", func(t *testing.T) {
		h := newHarness(t)
		if err := h.res.ResolveGroup("nope", true); !errors.Is(err, ErrGroupMissing) {
			t.Fatalf("err = %v, want ErrGroupMissing", err)
		}
	})

	t.Run("invalid group config", func(t *testing.T) {
		h := newHarness(t)
		h.writeGroup(t, "bad", `{"v":1,"name":"bad","targets":[]}`)
		if err := h.res.ResolveGroup("bad", true); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestPool(t *testing.T) {
	h := newHarness(t,
		index.Repo{Name: "beta", Path: "/repo/beta"},
		index.Repo{Name: "alpha", Path: "/repo/alpha"},
	)
	h.writeSession(t, "zeta", `{"v":1,"name":"zeta","windows":[{"panes":[{"run":"a"}]}]}`)
	h.writeSession(t, "alpha", `{"v":1,"name":"alpha","windows":[{"panes":[{"run":"a"}]}]}`)
	if got := h.res.Pool(); !reflect.DeepEqual(got, []string{"alpha", "beta", "zeta"}) {
		t.Errorf("Pool = %v", got)
	}
	if got := h.res.Targets(); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Errorf("Targets = %v", got)
	}
}
