package providers

import (
	"context"
	"errors"
	"log"
	"os"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Defaults for ConcurrentAggregator. Both are overridable per instance.
const (
	// DefaultMaxResults caps the merged result list when the constructor is
	// given a non-positive maximum. Mirrors config.Config.MaxResults.
	DefaultMaxResults = 30
	// DefaultMinScore is the score a low-priority result (Category >= CatApp)
	// must reach to survive a non-empty query. It is deliberately small: the
	// fuzzy weights make any match anchored at a word start comfortably
	// positive, while a stray mid-word subsequence lands at or below zero.
	DefaultMinScore = 1
)

// ConcurrentAggregator is the production Aggregator: it fans a query out to
// every registered provider in parallel, merges what comes back, ranks it and
// caps the list.
//
// # Ranking contract
//
// Results are sorted by (-Score, Category, Title). Score is the primary key so
// that better matches always float up; Category breaks score ties into the
// order declared by the Cat* constants; Title makes the order total, and
// therefore stable across runs.
//
// # Shared-score contract
//
// Every repo-derived provider (sessions, GitHub, connectors, repos) scores the
// *same string* — the repo's basename — with the *same* fuzzy.Scorer. Scoring
// is deterministic, so those providers independently arrive at an identical
// score for a given repo without coordinating. The sort's Category tiebreak
// then collapses them into one fixed-order block:
//
//	"blacksh" →  Open blacksheep session      (CatSession)
//	             Open blacksheep on GitHub    (CatGitHub)
//	             Open blacksheep on Railway   (CatConnector)
//	             Open blacksheep directory    (CatDirectory)
//
// New repo-derived providers join that block for free by obeying the same
// rule: score the repo name, nothing else. A provider that scores its own
// title instead ("Open blacksheep on GitHub") would break the block.
//
// # Thresholding
//
// Weak matches from the noisy, high-cardinality categories (apps, processes,
// plugin results — anything with Category >= CatApp) are dropped below
// MinScore. Repo-derived categories are never thresholded: their providers
// already decided the repo matched, and dropping half a block would look like
// a bug. An empty query bypasses thresholding entirely — providers interpret
// it as "show your defaults" and their scores are meaningless there.
//
// # Failure and cancellation
//
// A provider error is logged and that provider's results are dropped; Query
// itself never fails, because one broken plugin must not blank the launcher.
// All providers share one derived context: when ctx is cancelled (the UI
// supersedes the query on the next keystroke) they are expected to return
// promptly, and Query returns as soon as they do.
type ConcurrentAggregator struct {
	registry *Registry

	// MaxResults caps the returned slice. Non-positive means unlimited.
	//
	// Assign it directly only before the aggregator is queried; once the
	// launcher is live use SetMaxResults, which is safe against the query
	// goroutines (`banshee reload` runs on the GTK main loop while a
	// superseded query is still in flight).
	MaxResults int
	// MinScore is the cutoff applied to Category >= CatApp results on
	// non-empty queries. Same rule as MaxResults: SetMinScore once live.
	MinScore int
	// Logger receives provider errors. Never nil after NewAggregator. Set it
	// before the first query.
	Logger *log.Logger

	// mu guards MaxResults and MinScore against a concurrent reload.
	mu sync.RWMutex
}

// SetMaxResults changes the result cap on a live aggregator.
func (a *ConcurrentAggregator) SetMaxResults(n int) {
	a.mu.Lock()
	a.MaxResults = n
	a.mu.Unlock()
}

// SetMinScore changes the low-priority score cutoff on a live aggregator.
func (a *ConcurrentAggregator) SetMinScore(n int) {
	a.mu.Lock()
	a.MinScore = n
	a.mu.Unlock()
}

// limits reads the reconfigurable tunables under the lock.
func (a *ConcurrentAggregator) limits() (maxResults, minScore int) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.MaxResults, a.MinScore
}

// NewAggregator returns a ConcurrentAggregator over reg, capping merged
// results at maxResults (non-positive → DefaultMaxResults).
func NewAggregator(reg *Registry, maxResults int) *ConcurrentAggregator {
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	return &ConcurrentAggregator{
		registry:   reg,
		MaxResults: maxResults,
		MinScore:   DefaultMinScore,
		Logger:     log.New(os.Stderr, "banshee: ", 0),
	}
}

// Compile-time proof that ConcurrentAggregator satisfies the frozen contract.
var _ Aggregator = (*ConcurrentAggregator)(nil)

// Query implements Aggregator.
func (a *ConcurrentAggregator) Query(ctx context.Context, q string) []Result {
	if a == nil || a.registry == nil {
		return nil
	}
	provs := a.registry.Providers()
	if len(provs) == 0 {
		return nil
	}
	maxResults, minScore := a.limits()

	// errgroup gives every provider one shared, cancellable context. The
	// per-provider funcs always return nil: an error must not cancel the
	// siblings, and Query has no error to return anyway.
	g, gctx := errgroup.WithContext(ctx)
	collected := make([][]Result, len(provs))
	for i, p := range provs {
		i, p := i, p
		g.Go(func() error {
			res, err := p.Query(gctx, q)
			if err != nil {
				// Cancellation is the normal end of a superseded query: the UI
				// cancels the previous generation on every debounced keystroke
				// and never joins it, so every provider reports ctx.Err() a
				// moment later. Logging that would bury the real provider
				// errors this log exists for under a few lines per keystroke.
				if !isCancellation(err) {
					a.logf("provider %s: %v", p.Name(), err)
				}
				return nil
			}
			collected[i] = res
			return nil
		})
	}
	_ = g.Wait()

	merged := a.merge(collected, q, minScore)
	sortResults(merged)
	if maxResults > 0 && len(merged) > maxResults {
		merged = merged[:maxResults]
	}
	return merged
}

// isCancellation reports whether err is the shared context being cancelled or
// timing out rather than a genuine provider failure.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// merge flattens per-provider results, applying the score threshold.
func (a *ConcurrentAggregator) merge(collected [][]Result, q string, minScore int) []Result {
	n := 0
	for _, rs := range collected {
		n += len(rs)
	}
	out := make([]Result, 0, n)
	threshold := q != ""
	for _, rs := range collected {
		for _, r := range rs {
			if threshold && r.Category >= CatApp && r.Score < minScore {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// sortResults applies the (-Score, Category, Title) ordering. Exposed as a
// helper so tests and alternate aggregators rank identically.
func sortResults(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Score != rs[j].Score {
			return rs[i].Score > rs[j].Score
		}
		if rs[i].Category != rs[j].Category {
			return rs[i].Category < rs[j].Category
		}
		return rs[i].Title < rs[j].Title
	})
}

func (a *ConcurrentAggregator) logf(format string, args ...any) {
	if a.Logger == nil {
		return
	}
	a.Logger.Printf(format, args...)
}
