// Package repos provides the CatDirectory rows of the launcher: "Open <name>
// directory" for every git repository internal/index knows about.
//
// Activating a row hands the repo path to xdg-open as a detached process
// (Action.Kind == providers.ActExecDetach), which opens it in the user's file
// manager without tying the child's lifetime to the daemon.
package repos

import (
	"context"
	"sort"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/index"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// OpenerBin is the command used to open a directory.
const OpenerBin = "xdg-open"

// Provider lists repo directories. Construct it with New.
type Provider struct {
	index index.Index

	// Score ranks a repo name against the query. Defaults to fuzzy.Score.
	// It must be the same Scorer the other repo-derived providers use so the
	// whole block for one repo shares a score — see
	// providers.ConcurrentAggregator for the contract.
	Score fuzzy.Scorer
	// Icon is applied to every emitted result.
	Icon providers.Icon
}

// New returns a directory Provider over idx. A nil index yields no results.
func New(idx index.Index) *Provider {
	return &Provider{
		index: idx,
		Score: fuzzy.Score,
		Icon:  providers.Icon{ThemeName: "folder-symbolic"},
	}
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "repos" }

// Query implements providers.Provider.
//
// Scoring uses the repo basename only — never the rendered title — so the
// score matches what the session/GitHub/connector providers compute for the
// same repo and the four rows sort into one contiguous block.
//
// An empty query returns nothing: the launcher's default view is recent and
// running things, not a dump of every repo on disk.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q == "" || p.index == nil {
		return nil, nil
	}

	repos := p.index.Repos()
	sorted := make([]index.Repo, len(repos))
	copy(sorted, repos)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Path < sorted[j].Path
	})

	score := p.scorer()
	out := make([]providers.Result, 0, len(sorted))
	for _, r := range sorted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r.Name == "" {
			continue
		}
		s, ok := score(q, r.Name)
		if !ok {
			continue
		}
		out = append(out, providers.Result{
			ID:       "repos:" + r.Path,
			Title:    "Open " + r.Name + " directory",
			Subtitle: r.Path,
			Icon:     p.Icon,
			Category: providers.CatDirectory,
			Score:    s,
			Action: providers.Action{
				Kind: providers.ActExecDetach,
				Argv: []string{OpenerBin, r.Path},
			},
		})
	}
	return out, nil
}

func (p *Provider) scorer() fuzzy.Scorer {
	if p.Score != nil {
		return p.Score
	}
	return fuzzy.Score
}
