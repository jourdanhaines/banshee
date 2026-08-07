package repos

import (
	"context"
	"errors"
	"testing"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

type fakeIndex struct{ repos []index.Repo }

func (f *fakeIndex) Repos() []index.Repo { return f.repos }
func (f *fakeIndex) Exact(name string) (index.Repo, bool) {
	for _, r := range f.repos {
		if r.Name == name {
			return r, true
		}
	}
	return index.Repo{}, false
}
func (f *fakeIndex) Refresh() error { return nil }
func (f *fakeIndex) Clear() error   { return nil }

func idx(repos ...index.Repo) *fakeIndex { return &fakeIndex{repos: repos} }

func repo(name, path string) index.Repo { return index.Repo{Name: name, Path: path} }

func titles(rs []providers.Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestQuery(t *testing.T) {
	tests := []struct {
		name  string
		repos []index.Repo
		query string
		want  []string
	}{
		{
			name:  "prefix match",
			repos: []index.Repo{repo("blacksheep", "/home/u/dev/blacksheep")},
			query: "blacksh",
			want:  []string{"Open blacksheep directory"},
		},
		{
			name:  "empty query returns nothing",
			repos: []index.Repo{repo("blacksheep", "/b"), repo("banshee", "/n")},
			query: "",
			want:  nil,
		},
		{
			name:  "no match",
			repos: []index.Repo{repo("blacksheep", "/b")},
			query: "zzzz",
			want:  nil,
		},
		{
			name:  "empty index",
			query: "anything",
			want:  nil,
		},
		{
			name: "multiple matches sorted by name",
			repos: []index.Repo{
				repo("sheepdog", "/s2"),
				repo("blacksheep", "/s1"),
				repo("sheepish", "/s3"),
			},
			query: "sheep",
			want: []string{
				"Open blacksheep directory",
				"Open sheepdog directory",
				"Open sheepish directory",
			},
		},
		{
			name: "same name different paths both emitted, path-sorted",
			repos: []index.Repo{
				repo("api", "/b/api"),
				repo("api", "/a/api"),
			},
			query: "api",
			want: []string{
				"Open api directory",
				"Open api directory",
			},
		},
		{
			name:  "unnamed repo skipped",
			repos: []index.Repo{repo("", "/x"), repo("api", "/a")},
			query: "api",
			want:  []string{"Open api directory"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(idx(tt.repos...))
			got, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if !equal(titles(got), tt.want) {
				t.Fatalf("titles = %v, want %v", titles(got), tt.want)
			}
		})
	}
}

func TestQueryResultShape(t *testing.T) {
	p := New(idx(repo("blacksheep", "/home/u/dev/blacksheep")))
	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	r := got[0]
	if r.Category != providers.CatDirectory {
		t.Errorf("Category = %d, want CatDirectory", r.Category)
	}
	if r.ID != "repos:/home/u/dev/blacksheep" {
		t.Errorf("ID = %q", r.ID)
	}
	if r.Subtitle != "/home/u/dev/blacksheep" {
		t.Errorf("Subtitle = %q", r.Subtitle)
	}
	if r.Action.Kind != providers.ActExecDetach {
		t.Errorf("Action.Kind = %q, want %q", r.Action.Kind, providers.ActExecDetach)
	}
	want := []string{OpenerBin, "/home/u/dev/blacksheep"}
	if !equal(r.Action.Argv, want) {
		t.Errorf("Argv = %v, want %v", r.Action.Argv, want)
	}
	if r.Score <= 0 {
		t.Errorf("Score = %d, want positive for a prefix match", r.Score)
	}
	if r.Icon.ThemeName == "" {
		t.Error("Icon.ThemeName empty")
	}
}

// TestSharedScore proves the repo NAME is what gets scored — the property that
// keeps this row at the same score as the session/GitHub/connector rows for
// the same repo, forming one block in the aggregator.
func TestSharedScore(t *testing.T) {
	var seen []string
	p := New(idx(repo("blacksheep", "/b")))
	p.Score = func(q, cand string) (int, bool) {
		seen = append(seen, cand)
		return 777, true
	}
	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !equal(seen, []string{"blacksheep"}) {
		t.Fatalf("scored candidates = %v, want [blacksheep]", seen)
	}
	if got[0].Score != 777 {
		t.Fatalf("Score = %d, want 777", got[0].Score)
	}
}

// TestSharedScoreMatchesSiblingProvider is the cross-provider assertion: an
// independent provider scoring the same repo name with the default Scorer
// arrives at the identical number without coordination.
func TestSharedScoreMatchesSiblingProvider(t *testing.T) {
	p := New(idx(repo("blacksheep", "/b")))
	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	sibling, ok := p.scorer()("blacksh", "blacksheep")
	if !ok || sibling != got[0].Score {
		t.Fatalf("sibling score = (%d, %v), provider score = %d", sibling, ok, got[0].Score)
	}
}

func TestQueryNilIndex(t *testing.T) {
	p := New(nil)
	got, err := p.Query(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got != nil {
		t.Fatalf("results = %v, want nil", titles(got))
	}
}

func TestQueryCancelled(t *testing.T) {
	p := New(idx(repo("a", "/a")))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestName(t *testing.T) {
	if got := New(nil).Name(); got != "repos" {
		t.Fatalf("Name = %q", got)
	}
}

func TestImplementsProvider(t *testing.T) {
	var _ providers.Provider = New(nil)
}
