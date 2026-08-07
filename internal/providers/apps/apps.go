// Package apps provides installed applications (freedesktop .desktop entries)
// as launcher results.
//
// The application list is read once at construction through a Source — by
// default GIOSource, which uses GIO's AppInfo API so NoDisplay, OnlyShowIn,
// TryExec and friends are honored for free — and cached until Reload is
// called. Queries are matched with an injected Scorer against the display
// name, generic name and keywords of every cached application.
//
// Activation does not go through exec-detach: a .desktop entry may need
// Terminal=true handling and field-code expansion, which only GIO knows how
// to do. Results therefore carry Action{Kind: ActAppLaunch, Argv: {desktopID}}
// and the launch dispatcher gains the matching handler via
// RegisterAppLaunchHandler.
package apps

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// ActAppLaunch is the Action.Kind emitted by this provider. Action.Argv holds
// exactly one element: the .desktop application ID to launch.
const ActAppLaunch = "app-launch"

// Scorer scores a candidate string against a query, reporting whether it
// matched at all. It mirrors the signature of internal/fuzzy.Score; the
// concrete implementation is injected so this package stays independent of
// the ranking implementation.
type Scorer func(query, candidate string) (int, bool)

// App is banshee's minimal view of a .desktop application entry. It exists so
// the provider can be tested without GIO.
type App struct {
	// ID is the desktop file ID, e.g. "org.mozilla.firefox.desktop".
	ID string
	// Name is the user-visible application name (Name= / GAppInfo display name).
	Name string
	// GenericName is the localized generic name, e.g. "Web Browser". May be empty.
	GenericName string
	// Description is the .desktop Comment= field. May be empty.
	Description string
	// Executable is the program name from Exec=, without arguments.
	Executable string
	// Commandline is the full Exec= command line.
	Commandline string
	// Keywords are the .desktop Keywords= entries. May be empty.
	Keywords []string
}

// Source lists the applications available for launching.
type Source interface {
	// Apps returns the current application list. Entries that should not be
	// shown in menus are expected to be filtered out already.
	Apps() ([]App, error)
}

// SourceFunc adapts a function to the Source interface.
type SourceFunc func() ([]App, error)

// Apps implements Source.
func (f SourceFunc) Apps() ([]App, error) { return f() }

// Default provider tuning.
const (
	// DefaultEmptyQueryLimit caps how many applications are shown for an empty
	// query (the launcher's idle state).
	DefaultEmptyQueryLimit = 8
)

// Option configures a Provider.
type Option func(*Provider)

// WithSource replaces the application source (tests, alternate backends).
func WithSource(s Source) Option { return func(p *Provider) { p.src = s } }

// WithMaxResults caps the number of results a non-empty query returns.
// Values <= 0 mean unlimited (the default).
func WithMaxResults(n int) Option {
	return func(p *Provider) { p.maxResults = n }
}

// WithMinScore drops matches scoring below min, keeping weak app noise out of
// repo- and session-dominated result lists.
func WithMinScore(min int) Option { return func(p *Provider) { p.minScore = min } }

// WithEmptyQueryLimit sets how many applications an empty query returns.
// Zero disables empty-query results entirely.
func WithEmptyQueryLimit(n int) Option {
	return func(p *Provider) {
		if n < 0 {
			n = 0
		}
		p.emptyLimit = n
	}
}

// Provider is the applications result provider. It is safe for concurrent use.
type Provider struct {
	score      Scorer
	src        Source
	maxResults int
	minScore   int
	emptyLimit int

	mu    sync.RWMutex
	cache []App
	err   error
}

var _ providers.Provider = (*Provider)(nil)

// New builds an applications provider and loads the application list. score
// must not be nil. A load failure is not fatal: it is reported by every Query
// until a later Reload succeeds.
func New(score Scorer, opts ...Option) *Provider {
	p := &Provider{
		score:      score,
		src:        GIOSource{},
		emptyLimit: DefaultEmptyQueryLimit,
	}
	for _, o := range opts {
		o(p)
	}
	_ = p.Reload()
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "apps" }

// Reload refreshes the cached application list from the Source. It is called
// once by New and should be called again when the desktop database changes
// (for example from the daemon's reload op).
func (p *Provider) Reload() error {
	apps, err := p.src.Apps()
	if err == nil {
		apps = dedupe(apps)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.err = err
		return err
	}
	p.cache, p.err = apps, nil
	return nil
}

// Apps returns a copy of the cached application list.
func (p *Provider) Apps() []App {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]App, len(p.cache))
	copy(out, p.cache)
	return out
}

// Query implements providers.Provider. An empty query returns the first
// applications in name order (capped by WithEmptyQueryLimit); a non-empty
// query returns fuzzy matches sorted by descending score, then name.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.RLock()
	apps, loadErr := p.cache, p.err
	p.mu.RUnlock()
	if loadErr != nil {
		return nil, loadErr
	}

	q = strings.TrimSpace(q)
	if q == "" {
		return p.defaults(apps), nil
	}

	type scored struct {
		app   App
		score int
	}
	matches := make([]scored, 0, 16)
	for i, a := range apps {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		s, ok := p.best(q, a)
		if !ok || s < p.minScore {
			continue
		}
		matches = append(matches, scored{app: a, score: s})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return lessName(matches[i].app, matches[j].app)
	})
	if p.maxResults > 0 && len(matches) > p.maxResults {
		matches = matches[:p.maxResults]
	}
	out := make([]providers.Result, 0, len(matches))
	for _, m := range matches {
		out = append(out, Result(m.app, m.score))
	}
	return out, nil
}

// best returns the highest score across an application's searchable fields.
func (p *Provider) best(q string, a App) (int, bool) {
	best, matched := 0, false
	consider := func(candidate string) {
		if candidate == "" {
			return
		}
		s, ok := p.score(q, candidate)
		if !ok {
			return
		}
		if !matched || s > best {
			best, matched = s, true
		}
	}
	consider(a.Name)
	consider(a.GenericName)
	for _, k := range a.Keywords {
		consider(k)
	}
	return best, matched
}

func (p *Provider) defaults(apps []App) []providers.Result {
	if p.emptyLimit == 0 || len(apps) == 0 {
		return nil
	}
	sorted := make([]App, len(apps))
	copy(sorted, apps)
	sort.SliceStable(sorted, func(i, j int) bool { return lessName(sorted[i], sorted[j]) })
	if len(sorted) > p.emptyLimit {
		sorted = sorted[:p.emptyLimit]
	}
	out := make([]providers.Result, 0, len(sorted))
	for _, a := range sorted {
		out = append(out, Result(a, 0))
	}
	return out
}

// Result converts an application to a launcher row with the given score. It is
// exported so other providers (for example plugins surfacing apps) can reuse
// the exact same row shape.
func Result(a App, score int) providers.Result {
	return providers.Result{
		ID:       "app:" + a.ID,
		Title:    a.Name,
		Subtitle: subtitle(a),
		Icon:     providers.Icon{AppID: a.ID},
		Category: providers.CatApp,
		Score:    score,
		Action: providers.Action{
			Kind: ActAppLaunch,
			Argv: []string{a.ID},
		},
	}
}

func subtitle(a App) string {
	for _, s := range []string{a.GenericName, a.Description, a.Commandline, a.Executable} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func lessName(a, b App) bool {
	ai, bi := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if ai != bi {
		return ai < bi
	}
	return a.ID < b.ID
}

// dedupe drops entries without an ID or name and keeps the first entry per ID.
func dedupe(in []App) []App {
	seen := make(map[string]struct{}, len(in))
	out := make([]App, 0, len(in))
	for _, a := range in {
		if a.ID == "" || a.Name == "" {
			continue
		}
		if _, dup := seen[a.ID]; dup {
			continue
		}
		seen[a.ID] = struct{}{}
		out = append(out, a)
	}
	return out
}
