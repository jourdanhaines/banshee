// Package emoji offers the in-launcher emoji picker: one "Emoji picker" row
// carrying a providers.Picker over an embedded Unicode/CLDR table.
package emoji

import (
	"context"
	_ "embed"
	"strings"
	"sync"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

//go:generate go run ./gen -out emoji.tsv

//go:embed emoji.tsv
var rawData string

// TriggerScore is what the picker row gets when the user typed the trigger:
// below calc's 1000 so arithmetic still answers first, far above any organic
// fuzzy score so the row lands at the top (cliphist/totp convention).
const TriggerScore = 800

const (
	resultID = "emoji:picker"
	title    = "Emoji picker"
	iconName = "face-smile-symbolic"
)

// dataset is the embedded table, parsed once per process. Every Provider and
// every query shares the returned slice, which is read-only per the
// providers.Picker contract.
var dataset = sync.OnceValue(func() []providers.PickerItem { return parse(rawData) })

// Provider surfaces the single "Emoji picker" row, either on the emoji
// trigger or by fuzzy-matching its title.
type Provider struct {
	score fuzzy.Scorer
	items []providers.PickerItem
}

// New returns the emoji provider. It parses the dataset eagerly so the first
// keystroke never pays for it.
func New(score fuzzy.Scorer) *Provider {
	return &Provider{score: score, items: dataset()}
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "emoji" }

// Query returns the picker row at TriggerScore when q starts with the
// trigger (the remainder seeds the picker's search), or at its fuzzy score
// when q prefix-matches the row title or "picker". Empty queries return nothing.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(q) == "" {
		return nil, nil
	}
	if on, rest := parseTrigger(q); on {
		return []providers.Result{p.row(TriggerScore, rest)}, nil
	}
	// Only a prefix-grade match surfaces the row: CatEmoji is never MinScore
	// thresholded, so any subsequence ("ice", "mic") would otherwise match.
	q = strings.TrimSpace(q)
	best, found := 0, false
	for _, cand := range []string{title, "picker"} {
		if s, ok := p.score(q, cand); ok && (!found || s > best) {
			best, found = s, true
		}
	}
	if !found || best < fuzzy.PrefixBonus {
		return nil, nil
	}
	// The untriggered path never outranks a trigger row.
	if best >= TriggerScore {
		best = TriggerScore - 1
	}
	return []providers.Result{p.row(best, "")}, nil
}

// row builds the picker result. Action stays zero: the launcher's picker page
// copies the chosen cell through ActClipboardCopy (launch.RegisterBuiltins),
// so this package registers no handler. Columns is left 0 so the UI owns the
// default width.
func (p *Provider) row(score int, query string) providers.Result {
	return providers.Result{
		ID:       resultID,
		Title:    title,
		Subtitle: "Search and copy emoji",
		Icon:     providers.Icon{ThemeName: iconName},
		Category: providers.CatEmoji,
		Score:    score,
		Picker: &providers.Picker{
			Title: "Emoji",
			Query: query,
			Items: p.items,
		},
	}
}

// parseTrigger recognizes the trigger keyword and splits off the picker's
// initial search. It mirrors cliphist: the keyword is any prefix of "emoji"
// at least as long as "emo" — the tool is named emoji, so typing it out must
// keep triggering. Matching is case-insensitive; the remainder keeps its case.
func parseTrigger(q string) (triggered bool, query string) {
	q = strings.TrimSpace(q)
	word, rest, _ := strings.Cut(q, " ")
	word = strings.ToLower(word)
	if len(word) < len("emo") || !strings.HasPrefix("emoji", word) {
		return false, ""
	}
	rest = strings.TrimSpace(rest)
	// Typing the row's own title ("emoji pick") opens the full grid.
	if rest != "" && strings.HasPrefix("picker", strings.ToLower(rest)) {
		rest = ""
	}
	return true, rest
}

// parse reads the generated TSV (format documented in its header). It never
// fails — malformed lines are skipped — because boot.New must never fail;
// TestDatasetInvariants guards the committed table instead.
func parse(data string) []providers.PickerItem {
	items := make([]providers.PickerItem, 0, 2048)
	for line := range strings.Lines(data) {
		line = strings.TrimRight(line, "\r\n")
		// Comments are "# …": the keycap base "#️⃣" also starts with '#'.
		if line == "" || line == "#" || strings.HasPrefix(line, "# ") {
			continue
		}
		if strings.HasPrefix(line, "\t") {
			v, l, _ := strings.Cut(line[1:], "\t")
			if len(items) == 0 || v == "" {
				continue
			}
			last := &items[len(items)-1]
			last.Variants = append(last.Variants, providers.PickerItem{Value: v, Label: l})
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 2 || f[0] == "" {
			continue
		}
		it := providers.PickerItem{Value: f[0], Label: f[1]}
		if len(f) == 3 && f[2] != "" {
			it.Keywords = strings.Split(f[2], "|")
		}
		items = append(items, it)
	}
	return items
}
