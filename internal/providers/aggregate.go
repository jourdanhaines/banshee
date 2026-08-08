package providers

import "context"

// Aggregator is the query surface the UI consumes: one call per keystroke,
// results already merged, ranked and capped. Implemented by
// ConcurrentAggregator in aggregator.go; the UI must depend only on this
// interface so alternate frontends and mocks stay possible.
type Aggregator interface {
	// Query fans out to all registered providers, merges their results and
	// returns them sorted by (-Score, Category, Title), capped at the
	// configured max. ctx is cancelled when a newer query supersedes this one.
	Query(ctx context.Context, q string) []Result
}
