package apps

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// substringScorer is a deterministic stand-in for internal/fuzzy.Score: a
// case-insensitive substring match scoring earlier and tighter matches higher.
func substringScorer(query, candidate string) (int, bool) {
	q, c := strings.ToLower(query), strings.ToLower(candidate)
	i := strings.Index(c, q)
	if i < 0 {
		return 0, false
	}
	return 100 - i - (len(c) - len(q)), true
}

func testApps() []App {
	return []App{
		{ID: "firefox.desktop", Name: "Firefox", GenericName: "Web Browser", Executable: "firefox", Commandline: "firefox %u"},
		{ID: "org.gnome.Nautilus.desktop", Name: "Files", GenericName: "File Manager", Keywords: []string{"folder", "browser"}},
		{ID: "term.desktop", Name: "Terminal", Description: "Run commands", Executable: "foot"},
		{ID: "alacritty.desktop", Name: "Alacritty", GenericName: "Terminal Emulator"},
	}
}

func newTestProvider(t *testing.T, apps []App, opts ...Option) *Provider {
	t.Helper()
	opts = append([]Option{WithSource(SourceFunc(func() ([]App, error) { return apps, nil }))}, opts...)
	return New(substringScorer, opts...)
}

func TestProviderQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		opts  []Option
		want  []string // result titles, in order
	}{
		{
			name:  "empty query returns every app alphabetically by default",
			query: "",
			want:  []string{"Alacritty", "Files", "Firefox", "Terminal"},
		},
		{
			name:  "empty query capped",
			query: "",
			opts:  []Option{WithEmptyQueryLimit(3)},
			want:  []string{"Alacritty", "Files", "Firefox"},
		},
		{
			name:  "empty query disabled",
			query: "",
			opts:  []Option{WithEmptyQueryLimit(0)},
			want:  nil,
		},
		{
			name:  "whitespace query counts as empty",
			query: "   ",
			opts:  []Option{WithEmptyQueryLimit(1)},
			want:  []string{"Alacritty"},
		},
		{
			name:  "matches display name",
			query: "fire",
			want:  []string{"Firefox"},
		},
		{
			name:  "matches generic name",
			query: "web brow",
			want:  []string{"Firefox"},
		},
		{
			name:  "matches keyword",
			query: "folder",
			want:  []string{"Files"},
		},
		{
			name:  "name beats generic name on score",
			query: "terminal",
			want:  []string{"Terminal", "Alacritty"},
		},
		{
			name:  "max results caps output",
			query: "e",
			opts:  []Option{WithMaxResults(2)},
			want:  []string{"Files", "Terminal"},
		},
		{
			name:  "min score filters weak matches",
			query: "terminal",
			opts:  []Option{WithMinScore(95)},
			want:  []string{"Terminal"},
		},
		{
			name:  "no match",
			query: "zzz",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider(t, testApps(), tt.opts...)
			got, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			var titles []string
			for _, r := range got {
				titles = append(titles, r.Title)
			}
			if !reflect.DeepEqual(titles, tt.want) {
				t.Fatalf("titles = %v, want %v", titles, tt.want)
			}
		})
	}
}

func TestProviderResultShape(t *testing.T) {
	p := newTestProvider(t, testApps())
	got, err := p.Query(context.Background(), "fire")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want 1", len(got))
	}
	r := got[0]
	want := providers.Result{
		ID:       "app:firefox.desktop",
		Title:    "Firefox",
		Subtitle: "Web Browser",
		Icon:     providers.Icon{AppID: "firefox.desktop"},
		Category: providers.CatApp,
		Score:    r.Score,
		Action:   providers.Action{Kind: ActAppLaunch, Argv: []string{"firefox.desktop"}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("result = %+v, want %+v", r, want)
	}
	if r.Score <= 0 {
		t.Fatalf("score = %d, want > 0", r.Score)
	}
}

func TestSubtitleFallback(t *testing.T) {
	tests := []struct {
		name string
		app  App
		want string
	}{
		{"generic name wins", App{GenericName: "Web Browser", Description: "d", Commandline: "c"}, "Web Browser"},
		{"description next", App{Description: "Run commands", Commandline: "c"}, "Run commands"},
		{"commandline next", App{Commandline: "foot -e x", Executable: "foot"}, "foot -e x"},
		{"executable last", App{Executable: "foot"}, "foot"},
		{"nothing", App{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subtitle(tt.app); got != tt.want {
				t.Fatalf("subtitle = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReload(t *testing.T) {
	apps := testApps()
	p := New(substringScorer, WithSource(SourceFunc(func() ([]App, error) { return apps, nil })))
	if got, _ := p.Query(context.Background(), "gimp"); len(got) != 0 {
		t.Fatalf("expected no gimp before reload, got %d", len(got))
	}
	apps = append(apps, App{ID: "gimp.desktop", Name: "GIMP"})
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got, err := p.Query(context.Background(), "gimp")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].Title != "GIMP" {
		t.Fatalf("after reload got %+v, want GIMP", got)
	}
}

func TestSourceErrorSurfacesUntilReloadSucceeds(t *testing.T) {
	fail := true
	p := New(substringScorer, WithSource(SourceFunc(func() ([]App, error) {
		if fail {
			return nil, errors.New("boom")
		}
		return testApps(), nil
	})))
	if _, err := p.Query(context.Background(), "fire"); err == nil {
		t.Fatal("expected error from failed load")
	}
	fail = false
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := p.Query(context.Background(), "fire"); err != nil {
		t.Fatalf("Query after successful reload: %v", err)
	}
}

func TestDedupeDropsInvalidAndDuplicateEntries(t *testing.T) {
	p := newTestProvider(t, []App{
		{ID: "a.desktop", Name: "Alpha"},
		{ID: "a.desktop", Name: "Alpha Duplicate"},
		{ID: "", Name: "No ID"},
		{ID: "b.desktop", Name: ""},
	})
	got := p.Apps()
	if len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("apps = %+v, want single Alpha", got)
	}
}

func TestQueryHonorsContextCancellation(t *testing.T) {
	p := newTestProvider(t, testApps())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "fire"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRegisterAppLaunchHandler(t *testing.T) {
	tests := []struct {
		name    string
		action  providers.Action
		wantID  string
		wantErr bool
	}{
		{"launches by desktop id", providers.Action{Kind: ActAppLaunch, Argv: []string{"firefox.desktop"}}, "firefox.desktop", false},
		{"missing argv", providers.Action{Kind: ActAppLaunch}, "", true},
		{"empty id", providers.Action{Kind: ActAppLaunch, Argv: []string{""}}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			d := launch.NewDispatcher()
			RegisterAppLaunchHandlerWith(d, LauncherFunc(func(id string) error {
				got = id
				return nil
			}))
			err := d.Dispatch(tt.action)
			if tt.wantErr {
				if !errors.Is(err, ErrNoAppID) {
					t.Fatalf("err = %v, want ErrNoAppID", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got != tt.wantID {
				t.Fatalf("launched %q, want %q", got, tt.wantID)
			}
		})
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	var p providers.Provider = newTestProvider(t, testApps())
	if p.Name() != "apps" {
		t.Fatalf("Name = %q, want apps", p.Name())
	}
}
