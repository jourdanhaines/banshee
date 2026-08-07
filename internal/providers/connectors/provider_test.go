package connectors

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// fakeIndex is a static index.Index for tests.
type fakeIndex struct{ repos []index.Repo }

func (f fakeIndex) Repos() []index.Repo { return f.repos }
func (f fakeIndex) Exact(name string) (index.Repo, bool) {
	for _, r := range f.repos {
		if r.Name == name {
			return r, true
		}
	}
	return index.Repo{}, false
}
func (f fakeIndex) Refresh() error { return nil }
func (f fakeIndex) Clear() error   { return nil }

// substringScorer is a deterministic stand-in for internal/fuzzy.Score.
func substringScorer(q, cand string) (int, bool) {
	if strings.Contains(strings.ToLower(cand), strings.ToLower(q)) {
		return 100 - len(cand), true
	}
	return 0, false
}

// writeRepoConf writes <dir>/.banshee/config.json.
func writeRepoConf(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".banshee"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RepoConfigPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildURL(t *testing.T) {
	spec := URLSpec{Template: "https://railway.com/project/{binding}", RequiresBinding: true}
	tests := []struct {
		name    string
		spec    URLSpec
		binding string
		want    string
		ok      bool
	}{
		{"binding substituted", spec, "abc-123", "https://railway.com/project/abc-123", true},
		{"absolute url verbatim", spec, "https://railway.com/project/abc/settings", "https://railway.com/project/abc/settings", true},
		{"absolute url other host verbatim", spec, "https://internal.example.com/x", "https://internal.example.com/x", true},
		{"scheme-less value is a binding", spec, "railway.com/project/x", "https://railway.com/project/railway.com/project/x", true},
		{"repo placeholder", URLSpec{Template: "https://ci.example.com/{repo}"}, "", "https://ci.example.com/blacksheep", true},
		{"path placeholder", URLSpec{Template: "file+x://host{path}"}, "", "file+x://host/home/dev/blacksheep", true},
		{"non-absolute template rejected", URLSpec{Template: "/relative/{binding}"}, "x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BuildURL(tt.spec, tt.binding, "blacksheep", "/home/dev/blacksheep")
			if ok != tt.ok || got != tt.want {
				t.Fatalf("BuildURL = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestProviderQuery(t *testing.T) {
	root := t.TempDir()
	blacksheep := filepath.Join(root, "blacksheep")
	other := filepath.Join(root, "otherproj")
	for _, d := range []string{blacksheep, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRepoConf(t, blacksheep, `{"v":1,"connectors":{"railway":"proj-42"},"future":"ignored"}`)

	idx := fakeIndex{repos: []index.Repo{
		{Name: "blacksheep", Path: blacksheep},
		{Name: "otherproj", Path: other},
	}}

	p := New(idx, substringScorer)
	p.SetOriginResolver(func(path string) (string, bool) {
		if path == blacksheep {
			return "https://github.com/jourdanhaines/blacksheep", true
		}
		return "", false
	})

	got, err := p.Query(context.Background(), "blacksh")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}

	gh := got[0]
	if gh.Category != providers.CatGitHub {
		t.Errorf("github category = %d, want %d", gh.Category, providers.CatGitHub)
	}
	if gh.Title != "Open blacksheep on GitHub" {
		t.Errorf("github title = %q", gh.Title)
	}
	if gh.Action.Kind != providers.ActURL || gh.Action.URL != "https://github.com/jourdanhaines/blacksheep" {
		t.Errorf("github action = %+v", gh.Action)
	}

	rw := got[1]
	if rw.Category != providers.CatConnector {
		t.Errorf("railway category = %d, want %d", rw.Category, providers.CatConnector)
	}
	if rw.Action.URL != "https://railway.com/project/proj-42" {
		t.Errorf("railway url = %q", rw.Action.URL)
	}
	if rw.Accent != "#a78bfa" {
		t.Errorf("railway accent = %q", rw.Accent)
	}
	if gh.Score != rw.Score {
		t.Errorf("repo-derived results must share the repo score: %d vs %d", gh.Score, rw.Score)
	}
}

func TestProviderQueryUnboundAndEmpty(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "plainrepo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := fakeIndex{repos: []index.Repo{{Name: "plainrepo", Path: repo}}}

	p := New(idx, substringScorer)
	p.SetOriginResolver(func(string) (string, bool) { return "", false })

	got, err := p.Query(context.Background(), "plain")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unbound repo with no origin should yield nothing, got %+v", got)
	}

	got, err = p.Query(context.Background(), "   ")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty query = (%+v, %v), want no results", got, err)
	}
}

func TestProviderAddManifests(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "blacksheep")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRepoConf(t, repo, `{"v":1,"connectors":{"sentry":"org/proj"}}`)
	idx := fakeIndex{repos: []index.Repo{{Name: "blacksheep", Path: repo}}}

	p := NewWith(idx, substringScorer, nil)
	p.SetOriginResolver(func(string) (string, bool) { return "", false })
	p.AddManifests(
		Manifest{V: 1, ID: "sentry", Name: "Sentry", Type: TypeURL, Dir: "/plugins/sentry", Icon: "sentry.svg",
			URL: &URLSpec{Template: "https://sentry.io/{binding}", RequiresBinding: true}},
		Manifest{V: 1, ID: "docs", Name: "Docs", Type: TypeURL,
			URL: &URLSpec{Template: "https://docs.example.com/{repo}"}},
		Manifest{V: 1, ID: "wifi", Type: TypeExec, Exec: &ExecSpec{Bin: "p"}}, // ignored
	)

	got, err := p.Query(context.Background(), "black")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}
	if got[0].Action.URL != "https://sentry.io/org/proj" {
		t.Errorf("sentry url = %q", got[0].Action.URL)
	}
	if got[0].Icon.Path != filepath.Join("/plugins/sentry", "sentry.svg") {
		t.Errorf("sentry icon = %+v", got[0].Icon)
	}
	if got[0].Title != "Open blacksheep on Sentry" {
		t.Errorf("default title = %q", got[0].Title)
	}
	// requires_binding false → shows without a binding.
	if got[1].Action.URL != "https://docs.example.com/blacksheep" {
		t.Errorf("docs url = %q", got[1].Action.URL)
	}
}

func TestProviderOverrideBuiltin(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "blacksheep")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	idx := fakeIndex{repos: []index.Repo{{Name: "blacksheep", Path: repo}}}

	p := New(idx, substringScorer)
	p.SetOriginResolver(func(string) (string, bool) { return "https://github.com/u/blacksheep", true })
	p.AddManifests(Manifest{V: 1, ID: IDGitHub, Name: "GH", Type: TypeURL,
		URL: &URLSpec{Template: "https://github.com/{binding}", Title: "GH: {repo}", RequiresBinding: true}})

	if n := len(p.Manifests()); n != 2 {
		t.Fatalf("override should replace in place, got %d manifests", n)
	}
	got, err := p.Query(context.Background(), "black")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Title != "GH: blacksheep" {
		t.Errorf("title = %q, want overridden", got[0].Title)
	}
	if got[0].Category != providers.CatGitHub {
		t.Errorf("category = %d, want CatGitHub preserved", got[0].Category)
	}
}

func TestProviderQueryCancelled(t *testing.T) {
	idx := fakeIndex{repos: []index.Repo{{Name: "blacksheep", Path: "/nope/blacksheep"}}}
	p := New(idx, substringScorer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "black"); err == nil {
		t.Fatal("expected context error")
	}
}

func TestLoadRepoConfig(t *testing.T) {
	dir := t.TempDir()
	if rc, err := LoadRepoConfig(dir); err != nil || len(rc.Connectors) != 0 {
		t.Fatalf("missing file = (%+v, %v), want empty config and no error", rc, err)
	}
	writeRepoConf(t, dir, `{"v":1,"connectors":{"railway":"x"},"unknown":{"a":1}}`)
	rc, err := LoadRepoConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rc.V != 1 || rc.Connectors["railway"] != "x" {
		t.Fatalf("got %+v", rc)
	}
	writeRepoConf(t, dir, `not json`)
	if _, err := LoadRepoConfig(dir); err == nil {
		t.Fatal("expected parse error")
	}
}
