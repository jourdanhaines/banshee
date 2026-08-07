package fuzzy

import "testing"

func TestScoreMatching(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		candidate string
		wantOK    bool
	}{
		{"empty query matches anything", "", "blacksheep", true},
		{"empty query matches empty candidate", "", "", true},
		{"empty candidate never matches", "bl", "", false},
		{"prefix", "blacksh", "blacksheep", true},
		{"exact", "blacksheep", "blacksheep", true},
		{"case insensitive query", "BLACKSH", "blacksheep", true},
		{"case insensitive candidate", "blacksh", "BlackSheep", true},
		{"subsequence with gaps", "bsp", "blacksheep", true},
		{"acronym across separators", "bd", "banshee-daemon", true},
		{"not a subsequence", "zzz", "blacksheep", false},
		{"out of order", "shblack", "blacksheep", false},
		{"query longer than candidate", "blacksheepy", "blacksheep", false},
		{"unicode", "café", "Café Noir", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := Score(tt.query, tt.candidate)
			if ok != tt.wantOK {
				t.Fatalf("Score(%q, %q) ok = %v, want %v", tt.query, tt.candidate, ok, tt.wantOK)
			}
		})
	}
}

func TestScoreEmptyQueryIsZero(t *testing.T) {
	got, ok := Score("", "anything")
	if !ok || got != 0 {
		t.Fatalf("Score(\"\", \"anything\") = (%d, %v), want (0, true)", got, ok)
	}
}

func TestScoreNoMatchIsZero(t *testing.T) {
	got, ok := Score("zzz", "blacksheep")
	if ok || got != 0 {
		t.Fatalf("Score(\"zzz\", \"blacksheep\") = (%d, %v), want (0, false)", got, ok)
	}
}

// TestScoreOrdering pins the relative ranking rules: exact beats prefix, which
// beats a word-start match, which beats a scattered subsequence.
func TestScoreOrdering(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		stronger string
		weaker   string
	}{
		{"exact over prefix", "blacksheep", "blacksheep", "blacksheepdocs"},
		{"prefix over infix", "sheep", "sheepdog", "blacksheep"},
		{"prefix over scattered", "blacksh", "blacksheep", "bulk-lack-stash"},
		{"contiguous over scattered", "bsh", "bshell", "blacksheep"},
		{"word start over midword", "d", "banshee-daemon", "loaded"},
		{"camelCase boundary over midword", "d", "bansheeDaemon", "loaded"},
		{"shallower leading offset", "sheep", "xsheep", "xxxxxsheep"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hi, ok := Score(tt.query, tt.stronger)
			if !ok {
				t.Fatalf("Score(%q, %q) did not match", tt.query, tt.stronger)
			}
			lo, ok := Score(tt.query, tt.weaker)
			if !ok {
				t.Fatalf("Score(%q, %q) did not match", tt.query, tt.weaker)
			}
			if hi <= lo {
				t.Fatalf("Score(%q, %q)=%d should beat Score(%q, %q)=%d",
					tt.query, tt.stronger, hi, tt.query, tt.weaker, lo)
			}
		})
	}
}

// TestScoreBlackshBlacksheep is the plan's worked example: the query that
// drives the "blacksheep" result block must produce a strong positive score.
func TestScoreBlackshBlacksheep(t *testing.T) {
	got, ok := Score("blacksh", "blacksheep")
	if !ok {
		t.Fatal(`Score("blacksh", "blacksheep") did not match`)
	}
	if got < PrefixBonus {
		t.Fatalf("score = %d, want >= PrefixBonus (%d)", got, PrefixBonus)
	}
	if exact, _ := Score("blacksheep", "blacksheep"); exact <= got {
		t.Fatalf("exact score %d must exceed prefix score %d", exact, got)
	}
}

// TestScoreDeterministic underpins the aggregator's shared-score contract:
// independent providers scoring the same name must land on the same number.
func TestScoreDeterministic(t *testing.T) {
	first, ok := Score("blacksh", "blacksheep")
	if !ok {
		t.Fatal("expected match")
	}
	for i := 0; i < 100; i++ {
		got, ok := Score("blacksh", "blacksheep")
		if !ok || got != first {
			t.Fatalf("iteration %d: got (%d, %v), want (%d, true)", i, got, ok, first)
		}
	}
}

// TestScoreThresholdSanity checks the assumption ConcurrentAggregator's
// MinScore relies on: matches anchored at a word start are positive, while a
// lone scattered mid-word match is not.
func TestScoreThresholdSanity(t *testing.T) {
	positive := []struct{ query, candidate string }{
		{"fire", "Firefox"},
		{"ff", "Firefox"},
		{"term", "gnome-terminal"},
		{"code", "Visual Studio Code"},
	}
	for _, c := range positive {
		got, ok := Score(c.query, c.candidate)
		if !ok || got < 1 {
			t.Errorf("Score(%q, %q) = (%d, %v), want positive match", c.query, c.candidate, got, ok)
		}
	}

	weak := []struct{ query, candidate string }{
		{"x", "unexpected"},
		{"o", "Firefox"},
	}
	for _, c := range weak {
		got, ok := Score(c.query, c.candidate)
		if ok && got >= 1 {
			t.Errorf("Score(%q, %q) = %d, want below threshold", c.query, c.candidate, got)
		}
	}
}

func TestScorerSeamAcceptsScore(t *testing.T) {
	var s Scorer = Score
	if _, ok := s("a", "abc"); !ok {
		t.Fatal("Scorer seam did not accept Score")
	}
}
