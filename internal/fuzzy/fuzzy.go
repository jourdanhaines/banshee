// Package fuzzy provides banshee's single ranking primitive: a Sublime-Text
// style subsequence matcher with a numeric score.
//
// Every provider scores candidates through this package so that ranking stays
// consistent across result categories. In particular, repo-derived results
// (session, GitHub, connectors, directory) all score the *same repo name* with
// the *same* Scorer and therefore share one score — which is what makes them
// collapse into a fixed-order block in the aggregator.
//
// The exported surface is deliberately tiny (Score and Scorer) so the matching
// engine can be swapped wholesale — for a third-party library or a frecency
// aware ranker — without touching a single provider. Only matchPositions and
// the bonus constants below implement the current engine.
package fuzzy

import (
	"strings"
	"unicode"
)

// Scorer is the swappable ranking seam. Providers hold a Scorer field rather
// than calling Score directly, so tests (and future ranking experiments) can
// substitute an alternative implementation.
type Scorer = func(string, string) (int, bool)

// Compile-time proof that Score satisfies the seam.
var _ Scorer = Score

// Scoring weights. Exported so callers can reason about thresholds (see
// providers.DefaultMinScore) instead of hardcoding magic numbers.
const (
	// ExactBonus is added when candidate equals query (case-insensitively).
	// An exact match also earns PrefixBonus, so exact always outranks prefix.
	ExactBonus = 1000
	// PrefixBonus is added when candidate starts with query.
	PrefixBonus = 500

	// FirstCharBonus rewards matching the candidate's first rune.
	FirstCharBonus = 10
	// SeparatorBonus rewards a match right after -, _, ., /, space, etc.
	SeparatorBonus = 20
	// CamelCaseBonus rewards a match on an upper-case rune that follows a
	// lower-case rune ("bansheeDaemon" matched at 'D').
	CamelCaseBonus = 20
	// AdjacentBonus rewards each match immediately following the previous
	// match, favoring contiguous runs.
	AdjacentBonus = 5
	// LeadingCharPenalty is charged per unmatched rune before the first
	// match, clamped at MaxLeadingCharPenalty.
	LeadingCharPenalty = -3
	// MaxLeadingCharPenalty clamps the cumulative leading penalty.
	MaxLeadingCharPenalty = -9
)

// Score reports how well query matches candidate.
//
// Matching is case-insensitive and subsequence based: every rune of query must
// appear in candidate in order, though not necessarily contiguously. ok is
// false when no such subsequence exists, in which case the score is 0 and must
// be ignored.
//
// An empty query matches everything and yields (0, true) — providers use that
// to decide their own "defaults" behavior rather than being handed every
// candidate at score zero.
//
// Higher scores are better. Scores are only comparable between candidates
// scored against the same query; they carry no absolute meaning beyond
// "positive means a reasonably strong match".
func Score(query, candidate string) (int, bool) {
	if query == "" {
		return 0, true
	}
	if candidate == "" {
		return 0, false
	}

	cand := []rune(candidate)
	lowerCand := make([]rune, len(cand))
	for i, r := range cand {
		lowerCand[i] = unicode.ToLower(r)
	}
	lowerQuery := []rune(strings.ToLower(query))

	positions, ok := matchPositions(lowerQuery, lowerCand)
	if !ok {
		return 0, false
	}

	score := positionScore(cand, positions)

	lc := string(lowerCand)
	lq := string(lowerQuery)
	if strings.HasPrefix(lc, lq) {
		score += PrefixBonus
		if len(lowerCand) == len(lowerQuery) {
			score += ExactBonus
		}
	}
	return score, true
}

// matchPositions greedily walks candidate left to right consuming query runes,
// returning the index in candidate of each matched query rune. Greedy (rather
// than optimal) matching keeps scoring O(len(candidate)) and — more
// importantly — deterministic, which the aggregator's shared-score contract
// relies on.
func matchPositions(query, candidate []rune) ([]int, bool) {
	positions := make([]int, 0, len(query))
	qi := 0
	for ci := 0; ci < len(candidate) && qi < len(query); ci++ {
		if candidate[ci] == query[qi] {
			positions = append(positions, ci)
			qi++
		}
	}
	if qi < len(query) {
		return nil, false
	}
	return positions, true
}

// positionScore turns match positions into a score using the bonus weights.
// It reads the original (un-lowered) candidate so camelCase boundaries survive.
func positionScore(candidate []rune, positions []int) int {
	if len(positions) == 0 {
		return 0
	}

	score := 0
	prev := -2
	for _, idx := range positions {
		if idx == 0 {
			score += FirstCharBonus
		} else {
			if isSeparator(candidate[idx-1]) {
				score += SeparatorBonus
			}
			if unicode.IsLower(candidate[idx-1]) && unicode.IsUpper(candidate[idx]) {
				score += CamelCaseBonus
			}
		}
		if idx == prev+1 {
			score += AdjacentBonus
		}
		prev = idx
	}

	penalty := positions[0] * LeadingCharPenalty
	if penalty < MaxLeadingCharPenalty {
		penalty = MaxLeadingCharPenalty
	}
	return score + penalty
}

// isSeparator reports whether r is a word boundary for scoring purposes.
func isSeparator(r rune) bool {
	switch r {
	case '-', '_', '.', '/', '\\', ' ', ':', '@', '+':
		return true
	}
	return unicode.IsSpace(r)
}
