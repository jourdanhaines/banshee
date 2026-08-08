package connectors

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

func repoList(paths ...string) []index.Repo {
	var out []index.Repo
	for _, p := range paths {
		out = append(out, index.Repo{Name: filepath.Base(p), Path: p})
	}
	return out
}

// fakeCurrentRepo returns a CurrentRepoFunc with a call counter.
func fakeCurrentRepo(root string, ok bool, calls *int) CurrentRepoFunc {
	return func(ctx context.Context) (string, string, bool) {
		*calls++
		return root, filepath.Base(root), ok
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLinkResults(t *testing.T) {
	ctx := context.Background()

	railway := Manifest{
		V: ManifestVersion, ID: "railway", Name: "Railway", Icon: "railway",
		Accent: "#a78bfa", Type: TypeURL,
		URL:      &URLSpec{Template: "https://railway.com/project/{binding}", Title: "Open {repo} on Railway", RequiresBinding: true},
		Category: providers.CatConnector,
	}
	github := Manifest{
		V: ManifestVersion, ID: "github", Name: "GitHub", Icon: "github", Type: TypeURL,
		URL:          &URLSpec{Template: "{binding}", Title: "Open {repo} on GitHub", RequiresBinding: true},
		Category:     providers.CatGitHub,
		DeriveOrigin: true,
	}
	noBinding := Manifest{
		V: ManifestVersion, ID: "docs", Name: "Docs", Type: TypeURL,
		URL:      &URLSpec{Template: "https://docs.example.com/{repo}", RequiresBinding: false},
		Category: providers.CatConnector,
	}

	t.Run("unbound repo emits a link row with a working form", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))

		out, err := p.Query(ctx, "railway")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 {
			t.Fatalf("got %d results, want 1", len(out))
		}
		r := out[0]
		wantTitle := "Link Railway project to " + filepath.Base(repo)
		if r.Title != wantTitle {
			t.Errorf("Title = %q, want %q", r.Title, wantTitle)
		}
		if r.ID != "connector-link:railway:"+repo {
			t.Errorf("ID = %q", r.ID)
		}
		if r.Category != providers.CatConnector || r.Accent != "#a78bfa" {
			t.Errorf("Category/Accent = %v/%q", r.Category, r.Accent)
		}
		if r.Icon.Builtin != "railway" {
			t.Errorf("Icon = %+v, want builtin railway", r.Icon)
		}
		if r.Form == nil {
			t.Fatal("Form is nil")
		}
		if len(r.Form.Fields) != 1 || r.Form.Fields[0].Key != "binding" || !r.Form.Fields[0].Required {
			t.Errorf("Fields = %+v", r.Form.Fields)
		}
		act, err := r.Form.Build(map[string]string{"binding": " proj-1 "})
		if err != nil {
			t.Fatal(err)
		}
		wantArgv := []string{"railway", repo, "proj-1"}
		if act.Kind != ActConnectorLink || len(act.Argv) != 3 {
			t.Fatalf("action = %+v", act)
		}
		for i := range wantArgv {
			if act.Argv[i] != wantArgv[i] {
				t.Fatalf("Argv = %v, want %v", act.Argv, wantArgv)
			}
		}
		if _, err := r.Form.Build(map[string]string{"binding": "  "}); err == nil {
			t.Error("blank binding: want error")
		}
	})

	t.Run("bound repo emits no link row", func(t *testing.T) {
		repo := newGitRepo(t)
		writeRepoConf(t, repo, `{"v":1,"connectors":{"railway":"proj"}}`)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		out, err := p.Query(ctx, "railway")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("got %d results, want 0", len(out))
		}
	})

	t.Run("derivable origin suppresses the github link row", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{github},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		p.SetOriginResolver(func(path string) (string, bool) { return "https://github.com/u/r", true })
		out, err := p.Query(ctx, "github")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("got %d results, want 0 (origin derivable)", len(out))
		}
	})

	t.Run("underivable origin yields a github link row", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{github},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		p.SetOriginResolver(func(path string) (string, bool) { return "", false })
		out, err := p.Query(ctx, "github")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 {
			t.Errorf("got %d results, want 1", len(out))
		}
	})

	t.Run("no WithCurrentRepo means no link rows", func(t *testing.T) {
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway})
		out, err := p.Query(ctx, "railway")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("got %d results, want 0", len(out))
		}
	})

	t.Run("currentRepo not ok means no link rows", func(t *testing.T) {
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo("", false, &calls)))
		out, err := p.Query(ctx, "railway")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("got %d results, want 0", len(out))
		}
	})

	t.Run("non-matching query never probes tmux", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		out, err := p.Query(ctx, "zzz")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 || calls != 0 {
			t.Errorf("results = %d, currentRepo calls = %d; want 0, 0", len(out), calls)
		}
	})

	t.Run("non-requires-binding connector emits no link row", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{noBinding},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		out, err := p.Query(ctx, "docs")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 {
			t.Errorf("got %d results, want 0", len(out))
		}
	})

	t.Run("empty query yields nothing", func(t *testing.T) {
		repo := newGitRepo(t)
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		out, err := p.Query(ctx, "  ")
		if err != nil {
			t.Fatal(err)
		}
		if out != nil {
			t.Errorf("got %v, want nil", out)
		}
	})

	t.Run("linking end-to-end replaces link row with open row", func(t *testing.T) {
		// Named so "railway" matches both the connector and the repo
		// basename — the link row must vanish once a binding exists even
		// while the open row appears.
		repo := filepath.Join(t.TempDir(), "railwayapp")
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		calls := 0
		p := NewWith(fakeIndex{}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))

		out, err := p.Query(ctx, "railway")
		if err != nil || len(out) != 1 || out[0].Form == nil {
			t.Fatalf("precondition: link row expected, got %v (err %v)", out, err)
		}
		if err := SaveRepoBinding(repo, "railway", "proj-42"); err != nil {
			t.Fatal(err)
		}
		p2 := NewWith(fakeIndex{repos: repoList(repo)}, substringScorer, []Manifest{railway},
			WithCurrentRepo(fakeCurrentRepo(repo, true, &calls)))
		out, err = p2.Query(ctx, "railway")
		if err != nil {
			t.Fatal(err)
		}
		var open, link bool
		for _, r := range out {
			if r.Form == nil {
				open = true
			} else {
				link = true
			}
		}
		if !open {
			t.Error("want an open row after linking")
		}
		if link {
			t.Error("link row still present after linking")
		}
	})
}
