package steam

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// substringScorer is a deterministic stand-in for internal/fuzzy.Score: a
// case-insensitive substring match scoring earlier and tighter matches
// higher.
func substringScorer(query, candidate string) (int, bool) {
	i := strings.Index(strings.ToLower(candidate), strings.ToLower(query))
	if i < 0 {
		return 0, false
	}
	return 100 - i, true
}

// failingStore is a storefront that always errors, for tests that must not
// depend on (or accidentally reach) the network.
func failingStore(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jsonStore serves a fixed storesearch body.
func jsonStore(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestProvider builds a provider over a fixture root with the given games,
// wired to a failing storefront unless an option overrides it.
func newTestProvider(t *testing.T, games []fakeApp, opts ...Option) *Provider {
	t.Helper()
	root := t.TempDir()
	writeLibraryFolders(t, root)
	for _, g := range games {
		writeManifest(t, root, g)
	}
	srv := failingStore(t)
	base := []Option{WithSteamRoot(root), WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client())}
	return New(substringScorer, append(base, opts...)...)
}

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

func TestQueryBareMatchEmitsBlock(t *testing.T) {
	p := newTestProvider(t, []fakeApp{{"105600", "Terraria", "4"}, {"1245620", "ELDEN RING", "4"}})
	got, err := p.Query(context.Background(), "terr")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{
		"Play Terraria",
		"Open Terraria in Steam library",
		"View Terraria on Steam store",
		"View Terraria on SteamDB",
	}
	if !equal(titles(got), want) {
		t.Fatalf("titles = %v, want %v", titles(got), want)
	}
	wantURLs := []string{
		"steam://rungameid/105600",
		"steam://nav/games/details/105600",
		"https://store.steampowered.com/app/105600",
		"https://steamdb.info/app/105600/",
	}
	wantCats := []providers.Category{
		providers.CatSteamPlay, providers.CatSteamLibrary,
		providers.CatSteamStorePage, providers.CatSteamDB,
	}
	for i, r := range got {
		if r.Action.Kind != providers.ActURL || r.Action.URL != wantURLs[i] {
			t.Errorf("row %d action = %+v, want url %s", i, r.Action, wantURLs[i])
		}
		if r.Category != wantCats[i] {
			t.Errorf("row %d category = %d, want %d", i, r.Category, wantCats[i])
		}
		if r.Score != got[0].Score {
			t.Errorf("row %d score = %d, want shared %d", i, r.Score, got[0].Score)
		}
	}
}

func TestQueryBareBehavior(t *testing.T) {
	games := []fakeApp{{"105600", "Terraria", "4"}}
	cases := []struct {
		name string
		q    string
		want int // result count
	}{
		{"miss returns nothing", "zzz", 0},
		{"empty returns nothing", "", 0},
		{"whitespace returns nothing", "   ", 0},
		{"match returns one block", "terr", 4},
	}
	p := newTestProvider(t, games)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.Query(context.Background(), tc.q)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("len = %d (%v), want %d", len(got), titles(got), tc.want)
			}
		})
	}
}

func TestQueryTriggerListsAllGamesAlphabetically(t *testing.T) {
	p := newTestProvider(t, []fakeApp{
		{"105600", "Terraria", "4"},
		{"1245620", "ELDEN RING", "4"},
	})
	for _, q := range []string{"steam", "STEAM", "Steam  "} {
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("Query(%q): %v", q, err)
		}
		if len(got) != 8 {
			t.Fatalf("Query(%q) len = %d (%v), want 8", q, len(got), titles(got))
		}
		if got[0].Title != "Play ELDEN RING" || got[4].Title != "Play Terraria" {
			t.Fatalf("Query(%q) order: %v", q, titles(got))
		}
		if got[0].Score != TriggerScore || got[4].Score != TriggerScore-1 {
			t.Fatalf("Query(%q) scores: %d, %d", q, got[0].Score, got[4].Score)
		}
	}
	// "steamx" is not the trigger.
	got, err := p.Query(context.Background(), "steamx")
	if err != nil {
		t.Fatalf("Query(steamx): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query(steamx) = %v, want none", titles(got))
	}
}

func TestQueryTriggerFilterWithStoreResults(t *testing.T) {
	srv := jsonStore(t, `{"total":3,"items":[
		{"type":"app","name":"Terraria","id":105600,"price":{"currency":"USD","initial":999,"final":999}},
		{"type":"app","name":"Terraria Clone","id":42,"price":{"currency":"USD","initial":1499,"final":1499}},
		{"type":"music","name":"Terraria OST","id":43}
	]}`)
	p := newTestProvider(t, []fakeApp{{"105600", "Terraria", "4"}},
		WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))

	got, err := p.Query(context.Background(), "steam terraria")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{
		"Play Terraria",
		"Open Terraria in Steam library",
		"View Terraria on Steam store",
		"View Terraria on SteamDB",
		"Terraria Clone", // installed 105600 skipped, music-typed 43 filtered
		"Search Steam store for 'terraria'",
	}
	if !equal(titles(got), want) {
		t.Fatalf("titles = %v, want %v", titles(got), want)
	}

	if got[0].Score <= TriggerScore {
		t.Errorf("installed block score = %d, want > %d", got[0].Score, TriggerScore)
	}
	store := got[4]
	if store.Score != StoreScore {
		t.Errorf("store row score = %d, want %d", store.Score, StoreScore)
	}
	if store.Subtitle != "$14.99" {
		t.Errorf("store row subtitle = %q", store.Subtitle)
	}
	if store.Action.URL != "https://store.steampowered.com/app/42" {
		t.Errorf("store row url = %q", store.Action.URL)
	}
	if store.AltAction == nil || store.AltAction.URL != "https://steamdb.info/app/42/" {
		t.Errorf("store row alt = %+v", store.AltAction)
	}
	search := got[5]
	if search.Score != SearchScore || search.Category != providers.CatSteamSearch {
		t.Errorf("search row = %+v", search)
	}
	if search.Action.URL != "https://store.steampowered.com/search/?term=terraria" {
		t.Errorf("search url = %q", search.Action.URL)
	}
}

func TestQueryTriggerFilterStoreFailureDegrades(t *testing.T) {
	p := newTestProvider(t, []fakeApp{{"105600", "Terraria", "4"}})
	got, err := p.Query(context.Background(), "steam terr")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []string{
		"Play Terraria",
		"Open Terraria in Steam library",
		"View Terraria on Steam store",
		"View Terraria on SteamDB",
		"Search Steam store for 'terr'",
	}
	if !equal(titles(got), want) {
		t.Fatalf("titles = %v, want %v", titles(got), want)
	}
}

func TestQueryHonorsContext(t *testing.T) {
	p := newTestProvider(t, []fakeApp{{"105600", "Terraria", "4"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "terr"); err == nil {
		t.Fatal("Query with canceled ctx returned nil error")
	}
}

func TestQueryMaxResultsCapsGames(t *testing.T) {
	p := newTestProvider(t, []fakeApp{
		{"1", "Game Alpha", "4"},
		{"2", "Game Beta", "4"},
		{"3", "Game Gamma", "4"},
	}, WithMaxResults(2))
	got, err := p.Query(context.Background(), "game")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("len = %d (%v), want 2 games x 4 rows", len(got), titles(got))
	}
}

func TestReloadPicksUpNewGame(t *testing.T) {
	root := t.TempDir()
	writeLibraryFolders(t, root)
	writeManifest(t, root, fakeApp{"105600", "Terraria", "4"})
	srv := failingStore(t)
	p := New(substringScorer, WithSteamRoot(root), WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))

	if got, _ := p.Query(context.Background(), "elden"); len(got) != 0 {
		t.Fatalf("pre-reload query = %v", titles(got))
	}
	writeManifest(t, root, fakeApp{"1245620", "ELDEN RING", "4"})
	if err := p.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	got, err := p.Query(context.Background(), "elden")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("post-reload query = %v, want ELDEN RING block", titles(got))
	}
}

func TestNoSteamInstallIsInert(t *testing.T) {
	p := New(substringScorer, WithSteamRoot(t.TempDir())) // root without steamapps
	got, err := p.Query(context.Background(), "terr")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Query = %v, want none", titles(got))
	}
	// The empty-root path (discovery found nothing) is equally inert.
	p2 := &Provider{score: substringScorer}
	if err := p2.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got, err := p2.Query(context.Background(), "terr"); err != nil || len(got) != 0 {
		t.Fatalf("empty-root Query = %v, %v", got, err)
	}
}

func TestGameIconFieldChoice(t *testing.T) {
	root := t.TempDir()
	writeLibraryFolders(t, root)
	writeManifest(t, root, fakeApp{"105600", "Terraria", "4"})
	writeManifest(t, root, fakeApp{"1245620", "ELDEN RING", "4"})
	iconFile := writeIconNested(t, root, "105600")
	srv := failingStore(t)
	p := New(substringScorer, WithSteamRoot(root), WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))

	got, err := p.Query(context.Background(), "terr")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got[0].Icon.Path != iconFile || got[0].Icon.Builtin != "" {
		t.Errorf("cached-art icon = %+v, want Path only", got[0].Icon)
	}
	got, err = p.Query(context.Background(), "elden")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got[0].Icon.Builtin != builtinIcon || got[0].Icon.Path != "" {
		t.Errorf("fallback icon = %+v, want Builtin only", got[0].Icon)
	}
}

// TestBlockOrderThroughAggregator wires the real fuzzy scorer and the real
// aggregator around the provider and asserts the four-row block survives
// sorting intact and above an equal-scoring CatApp row — the steam analogue
// of aggregator_block_test.go.
func TestBlockOrderThroughAggregator(t *testing.T) {
	root := t.TempDir()
	writeLibraryFolders(t, root)
	writeManifest(t, root, fakeApp{"105600", "Terraria", "4"})
	srv := failingStore(t)
	steamP := New(fuzzy.Score, WithSteamRoot(root), WithStoreBaseURL(srv.URL), WithStoreClient(srv.Client()))

	reg := providers.NewRegistry()
	// A CatKill row over the same name ties every block row's score; only the
	// Category tiebreak keeps it below the block.
	reg.Register(noise{cat: providers.CatKill, item: "Terraria"})
	reg.Register(steamP)

	agg := providers.NewAggregator(reg, 30)
	agg.Logger = log.New(io.Discard, "", 0)

	got := agg.Query(context.Background(), "terraria")
	want := []string{
		"Play Terraria",
		"Open Terraria in Steam library",
		"View Terraria on Steam store",
		"View Terraria on SteamDB",
		"Terraria",
	}
	if !equal(titles(got), want) {
		t.Fatalf("titles = %v, want %v", titles(got), want)
	}
}

// noise emits one row scored with the real fuzzy scorer.
type noise struct {
	cat  providers.Category
	item string
}

func (n noise) Name() string { return "noise" }

func (n noise) Query(ctx context.Context, q string) ([]providers.Result, error) {
	s, ok := fuzzy.Score(q, n.item)
	if !ok {
		return nil, nil
	}
	return []providers.Result{{ID: "noise:" + n.item, Title: n.item, Category: n.cat, Score: s}}, nil
}
