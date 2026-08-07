package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// fakeIndex is a static in-memory index.Index.
type fakeIndex struct{ repos []index.Repo }

func (f *fakeIndex) Repos() []index.Repo { return f.repos }

func (f *fakeIndex) Exact(name string) (index.Repo, bool) {
	var found index.Repo
	n := 0
	for _, r := range f.repos {
		if r.Name == name {
			found = r
			n++
		}
	}
	return found, n == 1
}

func (f *fakeIndex) Refresh() error { return nil }
func (f *fakeIndex) Clear() error   { return nil }

// fakeRunner records argv and replays a canned response.
type fakeRunner struct {
	out  string
	err  error
	args [][]string
}

func (f *fakeRunner) Run(args ...string) (string, error) {
	f.args = append(f.args, append([]string(nil), args...))
	return f.out, f.err
}

func mkSessionsDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func idx(repos ...index.Repo) *fakeIndex { return &fakeIndex{repos: repos} }

func repo(name, path string) index.Repo { return index.Repo{Name: name, Path: path} }

func titles(rs []providers.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func newProvider(t *testing.T, i index.Index, r *fakeRunner, dir string) *Provider {
	t.Helper()
	p := New(i, r, dir)
	p.Binary = "banshee"
	return p
}

func TestQueryMatches(t *testing.T) {
	tests := []struct {
		name         string
		repos        []index.Repo
		configFiles  []string
		query        string
		wantTitles   []string
		wantSubtitle map[string]string
	}{
		{
			name:       "repo target matches",
			repos:      []index.Repo{repo("blacksheep", "/home/u/dev/blacksheep")},
			query:      "blacksh",
			wantTitles: []string{"Open blacksheep session"},
			wantSubtitle: map[string]string{
				"Open blacksheep session": "/home/u/dev/blacksheep",
			},
		},
		{
			name:        "config-only target matches",
			configFiles: []string{"work.json"},
			query:       "work",
			wantTitles:  []string{"Open work session"},
			wantSubtitle: map[string]string{
				"Open work session": SubtitleSessionConfig,
			},
		},
		{
			name:        "repo path wins over config subtitle",
			repos:       []index.Repo{repo("banshee", "/home/u/dev/banshee")},
			configFiles: []string{"banshee.json"},
			query:       "banshee",
			wantTitles:  []string{"Open banshee session"},
			wantSubtitle: map[string]string{
				"Open banshee session": "/home/u/dev/banshee",
			},
		},
		{
			name:        "union of repos and configs, name-sorted",
			repos:       []index.Repo{repo("zeta", "/z"), repo("alpha", "/a")},
			configFiles: []string{"mid.json"},
			query:       "",
			wantTitles:  nil, // empty query is the defaults path, tested below
		},
		{
			name:        "non-json files ignored",
			configFiles: []string{"work.json", "notes.txt", "work.json.bak"},
			query:       "work",
			wantTitles:  []string{"Open work session"},
		},
		{
			name:       "no match",
			repos:      []index.Repo{repo("blacksheep", "/b")},
			query:      "zzzzz",
			wantTitles: nil,
		},
		{
			name:        "multiple matches sorted by name",
			repos:       []index.Repo{repo("sheepdog", "/s2"), repo("blacksheep", "/s1")},
			configFiles: []string{"sheepish.json"},
			query:       "sheep",
			wantTitles: []string{
				"Open blacksheep session",
				"Open sheepdog session",
				"Open sheepish session",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.query == "" {
				t.Skip("empty query covered by TestQueryDefaults")
			}
			dir := mkSessionsDir(t, tt.configFiles...)
			p := newProvider(t, idx(tt.repos...), &fakeRunner{}, dir)

			got, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			gotTitles := titles(got)
			if len(gotTitles) != len(tt.wantTitles) {
				t.Fatalf("titles = %v, want %v", gotTitles, tt.wantTitles)
			}
			for i := range gotTitles {
				if gotTitles[i] != tt.wantTitles[i] {
					t.Fatalf("titles = %v, want %v", gotTitles, tt.wantTitles)
				}
			}
			for _, r := range got {
				if want, ok := tt.wantSubtitle[r.Title]; ok && r.Subtitle != want {
					t.Errorf("%s subtitle = %q, want %q", r.Title, r.Subtitle, want)
				}
			}
		})
	}
}

func TestQueryResultShape(t *testing.T) {
	p := newProvider(t, idx(repo("blacksheep", "/home/u/dev/blacksheep")), &fakeRunner{}, t.TempDir())
	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	r := got[0]
	if r.Category != providers.CatSession {
		t.Errorf("Category = %d, want CatSession", r.Category)
	}
	if r.ID != "sessions:blacksheep" {
		t.Errorf("ID = %q", r.ID)
	}
	if r.Action.Kind != providers.ActTerminal {
		t.Errorf("Action.Kind = %q, want %q", r.Action.Kind, providers.ActTerminal)
	}
	wantArgv := []string{"banshee", "blacksheep"}
	if len(r.Action.Argv) != 2 || r.Action.Argv[0] != wantArgv[0] || r.Action.Argv[1] != wantArgv[1] {
		t.Errorf("Argv = %v, want %v", r.Action.Argv, wantArgv)
	}
	if r.Score <= 0 {
		t.Errorf("Score = %d, want positive for a prefix match", r.Score)
	}
	if r.Icon.ThemeName == "" {
		t.Error("Icon.ThemeName empty")
	}
}

// TestSharedScore is the aggregator's shared-score contract from this side:
// the score must be the fuzzy score of the repo NAME, not of the rendered
// title, so a sibling provider scoring the same name lands on the same number.
func TestSharedScore(t *testing.T) {
	var seen []string
	p := newProvider(t, idx(repo("blacksheep", "/b")), &fakeRunner{}, t.TempDir())
	p.Score = func(q, cand string) (int, bool) {
		seen = append(seen, cand)
		return 777, true
	}
	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(seen) != 1 || seen[0] != "blacksheep" {
		t.Fatalf("scored candidates = %v, want [blacksheep]", seen)
	}
	if got[0].Score != 777 {
		t.Fatalf("Score = %d, want 777", got[0].Score)
	}
}

func TestQueryDefaults(t *testing.T) {
	tests := []struct {
		name       string
		out        string
		err        error
		repos      []index.Repo
		wantTitles []string
		wantSubs   []string
	}{
		{
			name:       "running sessions become defaults",
			out:        "banshee\nblacksheep\n",
			repos:      []index.Repo{repo("banshee", "/home/u/dev/banshee")},
			wantTitles: []string{"Open banshee session", "Open blacksheep session"},
			wantSubs:   []string{"/home/u/dev/banshee", SubtitleRunning},
		},
		{
			name:       "blank lines and duplicates ignored",
			out:        "a\n\na\n b \n",
			wantTitles: []string{"Open a session", "Open b session"},
			wantSubs:   []string{SubtitleRunning, SubtitleRunning},
		},
		{
			name:       "no tmux server is not an error",
			err:        errors.New("no server running"),
			wantTitles: nil,
		},
		{
			name:       "empty output",
			out:        "",
			wantTitles: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{out: tt.out, err: tt.err}
			p := newProvider(t, idx(tt.repos...), runner, t.TempDir())

			got, err := p.Query(context.Background(), "")
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			gotTitles := titles(got)
			if len(gotTitles) != len(tt.wantTitles) {
				t.Fatalf("titles = %v, want %v", gotTitles, tt.wantTitles)
			}
			for i := range gotTitles {
				if gotTitles[i] != tt.wantTitles[i] {
					t.Fatalf("titles = %v, want %v", gotTitles, tt.wantTitles)
				}
				if tt.wantSubs != nil && got[i].Subtitle != tt.wantSubs[i] {
					t.Errorf("subtitle[%d] = %q, want %q", i, got[i].Subtitle, tt.wantSubs[i])
				}
			}
		})
	}
}

// TestDefaultsUsesListSessions pins the tmux argv so the provider keeps
// working against the Runner contract without a live server.
func TestDefaultsUsesListSessions(t *testing.T) {
	runner := &fakeRunner{out: "a\n"}
	p := newProvider(t, idx(), runner, t.TempDir())
	if _, err := p.Query(context.Background(), ""); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(runner.args) != 1 {
		t.Fatalf("runner called %d times, want 1", len(runner.args))
	}
	want := []string{"list-sessions", "-F", "#{session_name}"}
	got := runner.args[0]
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv = %v, want %v", got, want)
		}
	}
}

func TestQueryNoRunnerNoDefaults(t *testing.T) {
	p := New(idx(repo("a", "/a")), nil, t.TempDir())
	got, err := p.Query(context.Background(), "")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("results = %v, want none", titles(got))
	}
}

func TestQueryNilIndex(t *testing.T) {
	dir := mkSessionsDir(t, "work.json")
	p := newProvider(t, nil, &fakeRunner{}, dir)
	got, err := p.Query(context.Background(), "work")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Open work session" {
		t.Fatalf("results = %v", titles(got))
	}
}

func TestQueryMissingSessionsDir(t *testing.T) {
	p := newProvider(t, idx(repo("a", "/a")), &fakeRunner{}, filepath.Join(t.TempDir(), "nope"))
	got, err := p.Query(context.Background(), "a")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %v, want 1", titles(got))
	}
}

func TestQueryCancelled(t *testing.T) {
	p := newProvider(t, idx(repo("a", "/a")), &fakeRunner{}, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestQueryDeadlineExceeded(t *testing.T) {
	p := newProvider(t, idx(repo("a", "/a")), &fakeRunner{}, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := p.Query(ctx, "a"); err == nil {
		t.Fatal("want error on expired context")
	}
}

func TestName(t *testing.T) {
	if got := New(nil, nil, "").Name(); got != "sessions" {
		t.Fatalf("Name = %q", got)
	}
}

func TestBinaryFallback(t *testing.T) {
	p := New(idx(repo("a", "/a")), nil, "")
	p.Binary = ""
	got, err := p.Query(context.Background(), "a")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got[0].Action.Argv[0] != "banshee" {
		t.Fatalf("Argv[0] = %q, want banshee", got[0].Action.Argv[0])
	}
}

func TestImplementsProvider(t *testing.T) {
	var _ providers.Provider = New(nil, nil, "")
}
