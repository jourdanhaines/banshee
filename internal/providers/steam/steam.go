// Package steam provides installed Steam games as launcher results, plus a
// live Steam store search behind a "steam" trigger.
//
// The installed-game index is read at construction from Steam's own local
// files (libraryfolders.vdf and the per-library appmanifest_*.acf manifests)
// and cached until Reload is called — the same lifecycle as the apps
// provider, and installed games change about as often as the desktop
// database. Every matched game emits a fixed-order four-row block — Play,
// library, store page, SteamDB — the repo-block pattern: all four rows carry
// the game's shared score and the CatSteamPlay..CatSteamDB category tiebreak
// orders them. Like the repo block, two games tying on score interleave by
// category; accepted there, accepted here.
//
// Every action is a URL (steam:// deep links and https pages) dispatched by
// the builtin ActURL handler, so this package registers no handler of its
// own.
//
// The "steam" trigger additionally searches the Steam storefront. The fetch
// runs only under the trigger with a non-empty filter, is bounded by
// storeTimeout and the query ctx, and degrades to the installed rows plus
// the "Search Steam store" row on any failure — a network hiccup must never
// blank games that are sitting on disk.
package steam

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/jourdanhaines/banshee/internal/providers"
)

const (
	// TriggerScore is the base score under the "steam" trigger: below calc's
	// forced answer (1000), far above any organic fuzzy score, and matching
	// totp's trigger weight. The bare trigger lists every game at
	// TriggerScore minus its alphabetical position so blocks stay contiguous
	// and ordered; a filtered trigger adds the fuzzy score instead, keeping
	// every installed block above every store row.
	TriggerScore = 800
	// StoreScore is the base score of live store rows; row i scores
	// StoreScore-i, preserving the API's own relevance order.
	StoreScore = 400
	// SearchScore pins the "Search Steam store" row last within the
	// triggered block, mirroring totp's AddScore.
	SearchScore = 10
)

// builtinIcon names the Steam mark compiled into the binary
// (internal/icons/data/steam.svg), used wherever a game has no cached art.
const builtinIcon = "steam"

// Scorer scores a candidate string against a query, reporting whether it
// matched at all. It mirrors the signature of internal/fuzzy.Score; the
// concrete implementation is injected so this package stays independent of
// the ranking implementation.
type Scorer func(query, candidate string) (int, bool)

// Option configures a Provider.
type Option func(*Provider)

// WithSteamRoot overrides Steam-root discovery (tests point this at a
// t.TempDir() fixture tree; a nonstandard install could be wired through it
// from config one day).
func WithSteamRoot(dir string) Option {
	return func(p *Provider) {
		if dir != "" {
			p.root = dir
			p.discovered = true
		}
	}
}

// WithMaxResults caps how many games a query matches and how many store rows
// a triggered search returns. Values <= 0 mean unlimited (the default).
func WithMaxResults(n int) Option {
	return func(p *Provider) { p.maxResults = n }
}

// WithStoreClient overrides the HTTP client used for store searches. The
// default carries the storeTimeout bound; tests inject one pointed at a
// httptest server.
func WithStoreClient(c *http.Client) Option {
	return func(p *Provider) {
		if c != nil {
			p.client = c
		}
	}
}

// WithStoreBaseURL overrides the storefront origin the search API is queried
// at (tests point this at a httptest server). Row URLs always target the real
// store — only the API call is redirected.
func WithStoreBaseURL(u string) Option {
	return func(p *Provider) {
		if u != "" {
			p.storeBase = u
		}
	}
}

// Provider is the Steam result provider. It is safe for concurrent use.
type Provider struct {
	score      Scorer
	root       string
	discovered bool // root already resolved (by option), skip discovery
	maxResults int
	client     *http.Client
	storeBase  string

	mu    sync.RWMutex
	games []Game
	err   error
}

var _ providers.Provider = (*Provider)(nil)

// New builds a Steam provider and scans the installed games. score must not
// be nil. Steam not being installed is a normal empty state, not an error;
// a scan failure is reported by every Query until a later Reload succeeds.
func New(score Scorer, opts ...Option) *Provider {
	p := &Provider{
		score:     score,
		client:    &http.Client{Timeout: storeTimeout},
		storeBase: defaultStoreBase,
	}
	for _, o := range opts {
		o(p)
	}
	if !p.discovered {
		p.root = discoverRoot()
	}
	_ = p.Reload()
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "steam" }

// Reload refreshes the cached game list from the Steam library manifests. It
// is called once by New and again by the daemon's reload op, which is how a
// newly installed game appears.
func (p *Provider) Reload() error {
	if p.root == "" {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.games, p.err = nil, nil
		return nil
	}
	games, err := scanGames(p.root)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.err = err
		return err
	}
	p.games, p.err = games, nil
	return nil
}

// Query implements providers.Provider.
//
// Behavior, in the order the checks run:
//
//   - An empty query returns nothing: a four-row block per game would flood
//     the launcher's default list.
//   - Without the trigger, game names are fuzzy-matched and each match emits
//     its block; the aggregator's MinScore threshold drops weak matches, the
//     same as apps.
//   - "steam" alone lists every installed game, alphabetically, no network.
//   - "steam <filter>" matches installed games first, then queries the
//     storefront: store rows follow the installed blocks in API order, and
//     the "Search Steam store for '<filter>'" row comes last. A store fetch
//     failure silently drops only the store rows.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.RLock()
	games, loadErr := p.games, p.err
	p.mu.RUnlock()
	if loadErr != nil {
		return nil, loadErr
	}

	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	triggered, filter := parseTrigger(q)
	if !triggered {
		return p.matchBlocks(ctx, games, q, 0)
	}
	if filter == "" {
		out := make([]providers.Result, 0, 4*len(games))
		for i, g := range games {
			if p.maxResults > 0 && i >= p.maxResults {
				break
			}
			out = append(out, gameBlock(g, TriggerScore-i)...)
		}
		return out, nil
	}

	out, err := p.matchBlocks(ctx, games, filter, TriggerScore)
	if err != nil {
		return nil, err
	}
	out = append(out, p.storeResults(ctx, games, filter)...)
	out = append(out, searchRow(filter))
	return out, nil
}

// matchBlocks emits the block of every game whose name fuzzy-matches needle,
// at base plus the fuzzy score (base 0 for bare queries, TriggerScore under
// the trigger).
func (p *Provider) matchBlocks(ctx context.Context, games []Game, needle string, base int) ([]providers.Result, error) {
	var out []providers.Result
	matched := 0
	for i, g := range games {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		s, ok := p.score(needle, g.Name)
		if !ok {
			continue
		}
		if p.maxResults > 0 && matched >= p.maxResults {
			break
		}
		matched++
		out = append(out, gameBlock(g, base+s)...)
	}
	return out, nil
}

// storeResults fetches the store search rows for filter. Any failure —
// timeout, cancellation, HTTP or decode error — returns nil: the installed
// rows and the search row must paint regardless.
func (p *Provider) storeResults(ctx context.Context, games []Game, filter string) []providers.Result {
	items, err := p.storeSearch(ctx, filter)
	if err != nil {
		return nil
	}
	installed := make(map[string]bool, len(games))
	for _, g := range games {
		installed[g.AppID] = true
	}
	var out []providers.Result
	for _, it := range items {
		id := strconv.Itoa(it.ID)
		if installed[id] {
			continue // the installed block already covers this game
		}
		if p.maxResults > 0 && len(out) >= p.maxResults {
			break
		}
		out = append(out, providers.Result{
			ID:       "steam:storesearch:" + id,
			Title:    it.Name,
			Subtitle: formatPrice(it.Price),
			Icon:     providers.Icon{Builtin: builtinIcon},
			Category: providers.CatSteamStore,
			Score:    StoreScore - len(out),
			Action:   providers.Action{Kind: providers.ActURL, URL: storePageURL(id)},
			AltAction: &providers.Action{
				Kind: providers.ActURL, URL: steamDBURL(id),
			},
		})
	}
	return out
}

// gameBlock builds one installed game's four fixed-order rows, all carrying
// the same score so the category tiebreak orders them.
func gameBlock(g Game, score int) []providers.Result {
	icon := providers.Icon{Builtin: builtinIcon}
	if g.IconPath != "" {
		// Exactly one Icon field: the UI's resolution precedence would let a
		// theme or builtin name shadow the path.
		icon = providers.Icon{Path: g.IconPath}
	}
	row := func(idPrefix, title string, cat providers.Category, url string) providers.Result {
		return providers.Result{
			ID:       "steam:" + idPrefix + ":" + g.AppID,
			Title:    title,
			Icon:     icon,
			Category: cat,
			Score:    score,
			Action:   providers.Action{Kind: providers.ActURL, URL: url},
		}
	}
	return []providers.Result{
		row("play", "Play "+g.Name, providers.CatSteamPlay, "steam://rungameid/"+g.AppID),
		row("library", "Open "+g.Name+" in Steam library", providers.CatSteamLibrary, "steam://nav/games/details/"+g.AppID),
		row("store", "View "+g.Name+" on Steam store", providers.CatSteamStorePage, storePageURL(g.AppID)),
		row("db", "View "+g.Name+" on SteamDB", providers.CatSteamDB, steamDBURL(g.AppID)),
	}
}

// searchRow is the trailing "search the store in a browser" row, always last
// within the triggered block.
func searchRow(filter string) providers.Result {
	return providers.Result{
		ID:       "steam:search",
		Title:    "Search Steam store for '" + filter + "'",
		Icon:     providers.Icon{Builtin: builtinIcon},
		Category: providers.CatSteamSearch,
		Score:    SearchScore,
		Action: providers.Action{
			Kind: providers.ActURL,
			URL:  "https://store.steampowered.com/search/?term=" + url.QueryEscape(filter),
		},
	}
}

func storePageURL(appID string) string { return "https://store.steampowered.com/app/" + appID }
func steamDBURL(appID string) string   { return "https://steamdb.info/app/" + appID + "/" }

// parseTrigger recognizes the "steam" keyword and splits off whatever follows
// it. Matching is case-insensitive on the keyword only; the remainder keeps
// its original case because it is fed to the fuzzy scorer, which does its own
// folding. Same shape as totp's trigger.
func parseTrigger(q string) (triggered bool, filter string) {
	lower := strings.ToLower(q)
	if lower == "steam" {
		return true, ""
	}
	if strings.HasPrefix(lower, "steam ") {
		return true, strings.TrimSpace(q[len("steam "):])
	}
	return false, ""
}
