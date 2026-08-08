package providers_test

// This external test package wires the real fuzzy scorer to the real sessions
// and repos providers (plus fakes standing in for the GitHub and connector
// providers that agent C owns) and asserts the plan's worked example end to
// end: typing "blacksh" must produce the blacksheep block, in category order,
// above every weaker result.
//
// It lives outside package providers because sessions and repos import
// providers; an internal test would form an import cycle.

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/providers/repos"
	"github.com/jourdanhaines/banshee/internal/providers/sessions"
)

type staticIndex struct{ repos []index.Repo }

func (s *staticIndex) Repos() []index.Repo { return s.repos }
func (s *staticIndex) Exact(name string) (index.Repo, bool) {
	for _, r := range s.repos {
		if r.Name == name {
			return r, true
		}
	}
	return index.Repo{}, false
}
func (s *staticIndex) Refresh() error { return nil }
func (s *staticIndex) Clear() error   { return nil }

// repoScoped stands in for the GitHub / connector providers: like them, it
// scores the repo name with the shared Scorer and renders its own title.
type repoScoped struct {
	name     string
	idx      index.Index
	cat      providers.Category
	titleFmt func(string) string
}

func (r *repoScoped) Name() string { return r.name }

func (r *repoScoped) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if q == "" {
		return nil, nil
	}
	var out []providers.Result
	for _, repo := range r.idx.Repos() {
		s, ok := fuzzy.Score(q, repo.Name)
		if !ok {
			continue
		}
		out = append(out, providers.Result{
			ID:       r.name + ":" + repo.Name,
			Title:    r.titleFmt(repo.Name),
			Subtitle: repo.Path,
			Category: r.cat,
			Score:    s,
			Action:   providers.Action{Kind: providers.ActURL, URL: "https://example.invalid/" + repo.Name},
		})
	}
	return out, nil
}

// noiseProvider emits low-priority rows whose fuzzy score is genuine.
type noiseProvider struct {
	name  string
	cat   providers.Category
	items []string
}

func (n *noiseProvider) Name() string { return n.name }

func (n *noiseProvider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	var out []providers.Result
	for _, it := range n.items {
		s, ok := fuzzy.Score(q, it)
		if !ok {
			continue
		}
		out = append(out, providers.Result{
			ID: n.name + ":" + it, Title: it, Category: n.cat, Score: s,
		})
	}
	return out, nil
}

func TestBlackshBlockOrderEndToEnd(t *testing.T) {
	idx := &staticIndex{repos: []index.Repo{
		{Name: "blacksheep", Path: "/home/u/dev/blacksheep"},
		{Name: "banshee", Path: "/home/u/dev/banshee"},
	}}

	sessionsP := sessions.New(idx, nil, t.TempDir())
	githubP := &repoScoped{
		name: "github", idx: idx, cat: providers.CatGitHub,
		titleFmt: func(n string) string { return "Open " + n + " on GitHub" },
	}
	railwayP := &repoScoped{
		name: "railway", idx: idx, cat: providers.CatConnector,
		titleFmt: func(n string) string { return "Open " + n + " on Railway" },
	}
	reposP := repos.New(idx)
	// Both noise rows match "blacksh"; blackshd even ties the block's score,
	// so only the Category tiebreak keeps it below the block.
	appsP := &noiseProvider{name: "apps", cat: providers.CatApp,
		items: []string{"Black Sheep Player"}}
	procsP := &noiseProvider{name: "procs", cat: providers.CatKill,
		items: []string{"blackshd"}}

	reg := providers.NewRegistry()
	// Registration order is intentionally shuffled: ordering must come from
	// the sort, not from registration.
	reg.Register(procsP)
	reg.Register(reposP)
	reg.Register(appsP)
	reg.Register(railwayP)
	reg.Register(sessionsP)
	reg.Register(githubP)

	agg := providers.NewAggregator(reg, 30)
	agg.Logger = log.New(io.Discard, "", 0)

	got := agg.Query(context.Background(), "blacksh")
	var gotTitles []string
	for _, r := range got {
		gotTitles = append(gotTitles, r.Title)
	}

	want := []string{
		"Open blacksheep session",
		"Open blacksheep on GitHub",
		"Open blacksheep on Railway",
		"Open blacksheep directory",
	}
	if len(gotTitles) < len(want) {
		t.Fatalf("results = %v, want at least %v", gotTitles, want)
	}
	for i := range want {
		if gotTitles[i] != want[i] {
			t.Fatalf("results =\n  %v\nwant prefix\n  %v", gotTitles, want)
		}
	}

	// The four block rows must genuinely share one score — that is what makes
	// the Category tiebreak decide their order.
	base := got[0].Score
	for i := 0; i < len(want); i++ {
		if got[i].Score != base {
			t.Fatalf("block row %q score = %d, want shared %d", got[i].Title, got[i].Score, base)
		}
	}

	// "banshee" does not match "blacksh", so no second block leaked in.
	for _, tl := range gotTitles {
		if tl == "Open banshee session" {
			t.Fatalf("non-matching repo leaked into results: %v", gotTitles)
		}
	}
}

func TestWeakNoiseThresholded(t *testing.T) {
	idx := &staticIndex{repos: []index.Repo{{Name: "blacksheep", Path: "/b"}}}

	reg := providers.NewRegistry()
	reg.Register(repos.New(idx))
	reg.Register(&noiseProvider{
		name: "apps", cat: providers.CatApp,
		// "blacksh" is a subsequence of this, but a badly scattered one.
		items: []string{"bulky lacquered ashtray"},
	})

	agg := providers.NewAggregator(reg, 30)
	agg.Logger = log.New(io.Discard, "", 0)

	for _, r := range agg.Query(context.Background(), "blacksh") {
		if r.Category >= providers.CatApp {
			t.Fatalf("weak app result survived thresholding: %q score=%d", r.Title, r.Score)
		}
	}
}
