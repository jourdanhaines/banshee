// Package lastaction contributes the "resume what you were doing" row of the
// launcher's empty-query view: the target or group recorded in
// ~/.local/share/banshee/last_action, the same state `banshee -r` replays.
//
// It only ever emits on an empty query. Once the user starts typing, the
// session, connector and app providers own the list and a sticky resume row
// would just be noise.
package lastaction

import (
	"context"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/state"
)

// Score is the score given to the resume row. The aggregator sorts by
// (-Score, Category, Title) and every other empty-query row scores 0, so any
// positive value pins the resume row to the top of the default view.
const Score = 1

// Reader is the slice of state.Store this provider needs. *state.Store
// satisfies it; tests pass a stub.
type Reader interface {
	Read() (state.Action, error)
}

// Provider emits the last recorded action as a single launcher row.
// Construct it with New.
type Provider struct {
	// store supplies the recorded action. May be nil (provider emits nothing).
	store Reader

	// Binary is the banshee executable placed in the terminal argv. Defaults
	// to "banshee", resolved through $PATH.
	Binary string
	// Icon is applied to the emitted result.
	Icon providers.Icon
}

// New returns a Provider reading from store. A nil store is valid and yields
// no results, so a broken state file can never break the launcher.
func New(store Reader) *Provider {
	return &Provider{
		store:  store,
		Binary: "banshee",
		Icon:   providers.Icon{ThemeName: "document-open-recent-symbolic"},
	}
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "lastaction" }

// Query implements providers.Provider. It returns at most one result, and
// only for the empty query.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if q != "" || p.store == nil {
		return nil, nil
	}
	act, err := p.store.Read()
	if err != nil || act.Name == "" {
		// No previous action (or an unreadable state file) is a normal empty
		// state, not a provider failure.
		return nil, nil
	}

	bin := p.Binary
	if bin == "" {
		bin = "banshee"
	}

	var title string
	var argv []string
	switch act.Kind {
	case state.KindGroup:
		title = "Resume group " + act.Name
		argv = []string{bin, "-g", act.Name}
	case state.KindTarget:
		title = "Resume " + act.Name
		argv = []string{bin, act.Name}
	default:
		return nil, nil
	}

	return []providers.Result{{
		ID:       "lastaction:" + act.String(),
		Title:    title,
		Subtitle: "last action",
		Icon:     p.Icon,
		Category: providers.CatSession,
		Score:    Score,
		Action: providers.Action{
			Kind: providers.ActTerminal,
			Argv: argv,
		},
	}}, nil
}
