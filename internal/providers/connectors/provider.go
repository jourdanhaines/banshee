package connectors

import (
	"context"
	"net/url"
	"strings"
	"sync"

	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// Scorer scores a query against a candidate string, reporting ok == false when
// the candidate does not match at all. It is an alias of the plain function
// signature so any compatible scorer (internal/fuzzy.Score) can be passed
// without an import.
type Scorer = func(query, candidate string) (int, bool)

// Provider emits connector results for every repository matching the query.
// All results derived from one repo carry that repo's fuzzy score, so the
// aggregator groups them into a fixed-order block under the repo's session and
// directory results.
type Provider struct {
	idx         index.Index
	score       Scorer
	origin      OriginResolver
	currentRepo CurrentRepoFunc // immutable after construction; nil disables link rows

	mu        sync.RWMutex
	manifests []Manifest

	origins *originCache
	confs   *repoConfCache
}

// Option configures optional Provider behavior.
type Option func(*Provider)

// WithCurrentRepo enables "Link <Connector> project to <repo>" results for
// the repo under the most recently active tmux pane. Without it the provider
// emits no link rows (tests, front-ends without tmux).
func WithCurrentRepo(fn CurrentRepoFunc) Option {
	return func(p *Provider) { p.currentRepo = fn }
}

// New returns a Provider seeded with the compiled-in connectors (Builtins).
// idx supplies the repositories to match against; score ranks repo basenames.
func New(idx index.Index, score Scorer, opts ...Option) *Provider {
	return NewWith(idx, score, Builtins(), opts...)
}

// NewWith returns a Provider using exactly the given manifests. Non-url
// manifests are ignored.
func NewWith(idx index.Index, score Scorer, manifests []Manifest, opts ...Option) *Provider {
	p := &Provider{
		idx:       idx,
		score:     score,
		manifests: append([]Manifest(nil), manifests...),
		confs:     newRepoConfCache(LoadRepoConfig),
	}
	p.origins = newOriginCache(DeriveOrigin)
	p.origin = p.origins.get
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "connectors" }

// AddManifests appends url-type manifests (typically loaded by the plugin
// host) to the connector list. Manifests whose id is already registered
// replace the existing entry in place, so a user plugin can override a
// built-in connector. Non-url manifests are ignored.
func (p *Provider) AddManifests(ms ...Manifest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range ms {
		if m.Type != TypeURL || m.URL == nil {
			continue
		}
		if m.Category == 0 {
			m.Category = providers.CatConnector
		}
		replaced := false
		for i := range p.manifests {
			if p.manifests[i].ID == m.ID {
				// Preserve built-in behavior flags the user manifest cannot
				// express (GitHub's origin derivation, its category).
				m.DeriveOrigin = m.DeriveOrigin || p.manifests[i].DeriveOrigin
				m.Category = p.manifests[i].Category
				p.manifests[i] = m
				replaced = true
				break
			}
		}
		if !replaced {
			p.manifests = append(p.manifests, m)
		}
	}
}

// SetManifests replaces the whole manifest list (used on reload).
func (p *Provider) SetManifests(ms []Manifest) {
	p.mu.Lock()
	p.manifests = nil
	p.mu.Unlock()
	p.AddManifests(ms...)
}

// Manifests returns a copy of the active manifest list, in display order.
func (p *Provider) Manifests() []Manifest {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Manifest(nil), p.manifests...)
}

// SetOriginResolver overrides how the GitHub connector derives a repo's URL.
// Intended for tests; the default shells out to git and caches per repo.
func (p *Provider) SetOriginResolver(r OriginResolver) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.origin = r
}

// Query implements providers.Provider. An empty query returns nothing:
// connectors are always anchored to a repo match.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	q = strings.TrimSpace(q)
	if q == "" || p.idx == nil || p.score == nil {
		return nil, nil
	}
	p.mu.RLock()
	manifests := append([]Manifest(nil), p.manifests...)
	origin := p.origin
	p.mu.RUnlock()

	var out []providers.Result
	for _, repo := range p.idx.Repos() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		score, ok := p.score(q, repo.Name)
		if !ok {
			continue
		}
		conf := p.confs.get(repo.Path)
		for _, m := range manifests {
			if m.Type != TypeURL || m.URL == nil {
				continue
			}
			binding := conf.Connectors[m.ID]
			if binding == "" && m.DeriveOrigin && origin != nil {
				if u, ok := origin(repo.Path); ok {
					binding = u
				}
			}
			if binding == "" && m.URL.RequiresBinding {
				continue
			}
			target, ok := BuildURL(*m.URL, binding, repo.Name, repo.Path)
			if !ok {
				continue
			}
			out = append(out, providers.Result{
				ID:       "connector:" + m.ID + ":" + repo.Path,
				Title:    connectorTitle(m, binding, repo.Name, repo.Path),
				Subtitle: target,
				Icon:     ResolveIcon(m.Icon, m.Dir),
				Category: m.Category,
				Score:    score,
				Accent:   m.Accent,
				Action:   providers.Action{Kind: providers.ActURL, URL: target},
			})
		}
	}
	links, err := p.linkResults(ctx, q, manifests, origin)
	if err != nil {
		return nil, err
	}
	return append(out, links...), nil
}

// linkResults emits one "Link <Name> project to <repo>" row per
// requires_binding url connector whose name or id fuzzy-matches q, when the
// repo under the most recently active tmux pane has no binding for it — and,
// for DeriveOrigin connectors, no derivable origin: the exact complement of
// the skip in the open-row loop above, so the two rows never coexist.
//
// Shared-score contract note: this row is connector-derived, not
// repo-derived — it is scored against the connector name/id rather than a
// repo basename, and therefore deliberately sits outside any repo block. It
// only appears when the query matches a connector name ("railway"), and at
// CatConnector it is exempt from the aggregator's MinScore threshold.
func (p *Provider) linkResults(ctx context.Context, q string, manifests []Manifest, origin OriginResolver) ([]providers.Result, error) {
	if p.currentRepo == nil {
		return nil, nil
	}
	// Collect matching candidates before touching tmux, so keystrokes that
	// match no connector name cost nothing.
	type candidate struct {
		m     Manifest
		score int
	}
	var cands []candidate
	for _, m := range manifests {
		if m.Type != TypeURL || m.URL == nil || !m.URL.RequiresBinding {
			continue
		}
		best, ok := p.score(q, m.Name)
		if s, idOK := p.score(q, m.ID); idOK && (!ok || s > best) {
			best, ok = s, true
		}
		if ok {
			cands = append(cands, candidate{m: m, score: best})
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, name, ok := p.currentRepo(ctx)
	if !ok {
		return nil, nil
	}
	conf := p.confs.get(root)
	var out []providers.Result
	for _, c := range cands {
		m := c.m
		binding := conf.Connectors[m.ID]
		if binding == "" && m.DeriveOrigin && origin != nil {
			if u, ok := origin(root); ok {
				binding = u
			}
		}
		if binding != "" {
			continue
		}
		id, repoRoot := m.ID, root
		out = append(out, providers.Result{
			ID:       "connector-link:" + m.ID + ":" + root,
			Title:    "Link " + m.Name + " project to " + name,
			Subtitle: root,
			Icon:     ResolveIcon(m.Icon, m.Dir),
			Category: m.Category,
			Score:    c.score,
			Accent:   m.Accent,
			Form: &providers.Form{
				Title: "Link " + m.Name + " to " + name,
				Fields: []providers.FormField{{
					Key:         "binding",
					Label:       m.Name + " project URL or ID",
					Placeholder: m.URL.Template,
					Required:    true,
				}},
				Build: func(values map[string]string) (providers.Action, error) {
					b := strings.TrimSpace(values["binding"])
					if b == "" {
						return providers.Action{}, errBindingRequired
					}
					return providers.Action{Kind: ActConnectorLink, Argv: []string{id, repoRoot, b}}, nil
				},
			},
		})
	}
	return out, nil
}

// BuildURL resolves a connector's target URL for one repo. A binding that is
// itself an absolute URL is used verbatim; otherwise it is substituted into
// the template as {binding}. ok is false when the result is not a usable
// absolute URL.
func BuildURL(spec URLSpec, binding, repoName, repoPath string) (string, bool) {
	if IsAbsoluteURL(binding) {
		return binding, true
	}
	target := expand(spec.Template, map[string]string{
		"binding": binding,
		"repo":    repoName,
		"path":    repoPath,
	})
	if !IsAbsoluteURL(target) {
		return "", false
	}
	return target, true
}

// IsAbsoluteURL reports whether v is an absolute URL with a scheme and host,
// i.e. something that can be handed to the system URL opener as-is.
func IsAbsoluteURL(v string) bool {
	v = strings.TrimSpace(v)
	if !strings.Contains(v, "://") {
		return false
	}
	u, err := url.Parse(v)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func connectorTitle(m Manifest, binding, repoName, repoPath string) string {
	tmpl := ""
	if m.URL != nil {
		tmpl = m.URL.Title
	}
	if strings.TrimSpace(tmpl) == "" {
		tmpl = "Open {repo} on {name}"
	}
	return expand(tmpl, map[string]string{
		"repo":    repoName,
		"path":    repoPath,
		"binding": binding,
		"name":    m.Name,
	})
}
