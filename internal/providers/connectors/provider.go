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
	idx    index.Index
	score  Scorer
	origin OriginResolver

	mu        sync.RWMutex
	manifests []Manifest

	origins *originCache
	confs   *repoConfCache
}

// New returns a Provider seeded with the compiled-in connectors (Builtins).
// idx supplies the repositories to match against; score ranks repo basenames.
func New(idx index.Index, score Scorer) *Provider {
	return NewWith(idx, score, Builtins())
}

// NewWith returns a Provider using exactly the given manifests. Non-url
// manifests are ignored.
func NewWith(idx index.Index, score Scorer, manifests []Manifest) *Provider {
	p := &Provider{
		idx:       idx,
		score:     score,
		manifests: append([]Manifest(nil), manifests...),
		confs:     newRepoConfCache(LoadRepoConfig),
	}
	p.origins = newOriginCache(DeriveOrigin)
	p.origin = p.origins.get
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
