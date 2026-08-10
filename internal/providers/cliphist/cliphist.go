package cliphist

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// Scorer scores a candidate string against a query, reporting whether it
// matched at all. Injected like totp.Scorer/procs.Scorer so this package
// stays independent of the ranking implementation.
type Scorer func(query, candidate string) (int, bool)

// TriggerScore is what every history row gets when the user typed the bare
// trigger: below calc's 1000 so arithmetic still answers first, far above any
// organic fuzzy score so the history block stays together. Recency ordering
// inside the block is Score = TriggerScore - position; past ~800 entries that
// goes negative, which is harmless — the aggregator sort is pure
// (-Score, Category, Title) and its MinScore threshold only applies to
// CatApp-and-later categories.
const TriggerScore = 800

// Icon theme names for rows that do not carry their own thumbnail.
const (
	iconText      = "edit-copy-symbolic"
	iconFiles     = "folder-symbolic"
	iconSensitive = "dialog-password-symbolic"
)

// Option configures a Provider.
type Option func(*Provider)

// WithNow overrides the clock used for "5m ago" subtitles; tests fix it.
func WithNow(now func() time.Time) Option {
	return func(p *Provider) {
		if now != nil {
			p.now = now
		}
	}
}

// Provider renders the clipboard history behind the "clip"/"cb" trigger. It
// holds no state of its own: every query reads the shared Store the watcher
// appends to.
type Provider struct {
	store *Store
	score Scorer
	now   func() time.Time
}

// New returns the clipboard-history provider reading store.
func New(store *Store, score Scorer, opts ...Option) *Provider {
	p := &Provider{store: store, score: score, now: time.Now}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "clipboard" }

// Query implements providers.Provider. Untriggered queries return nothing:
// clipboard contents must never surface in the idle list or mixed into
// unrelated searches — the user asks for them by name.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	triggered, filter := parseTrigger(q)
	if !triggered {
		return nil, nil
	}

	entries := p.store.List()
	now := p.now()
	var out []providers.Result
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		score := TriggerScore - i
		if filter != "" {
			// Sensitive entries are excluded from filtered queries: fuzzy
			// matching against masked content would let anyone watching the
			// screen binary-search the secret by which queries keep the row.
			if e.Sensitive {
				continue
			}
			s, ok := p.score(filter, matchText(e))
			if !ok || s <= 0 {
				continue
			}
			score = s
		}
		out = append(out, p.entryResult(e, score, now))
	}
	return out, nil
}

// entryResult renders one history entry as a launcher row.
func (p *Provider) entryResult(e Entry, score int, now time.Time) providers.Result {
	res := providers.Result{
		ID:       "clip:" + strconv.FormatUint(e.ID, 10),
		Title:    entryTitle(e),
		Subtitle: entrySubtitle(e, now),
		Icon:     entryIcon(e),
		Category: providers.CatClipboard,
		Score:    score,
		Action: providers.Action{
			Kind: ActClipCopy,
			Argv: []string{strconv.FormatUint(e.ID, 10)},
		},
		AltAction: &providers.Action{
			Kind: ActClipDelete,
			Argv: []string{strconv.FormatUint(e.ID, 10)},
		},
	}
	return res
}

// entryTitle is the row's single visible line. Sensitive text is masked;
// plain text keeps its first line only (the label is single-line anyway, this
// just stops a leading blank line from rendering an empty title).
func entryTitle(e Entry) string {
	switch e.Kind {
	case KindImage:
		return "Copied image"
	case KindFiles:
		paths := parseURIList(e.Text)
		switch len(paths) {
		case 0:
			return "Copied files"
		case 1:
			return filepath.Base(paths[0])
		default:
			return fmt.Sprintf("%s +%d more", filepath.Base(paths[0]), len(paths)-1)
		}
	default:
		if e.Sensitive {
			return maskTitle(e)
		}
		return firstLine(e.Text)
	}
}

// entrySubtitle is the row's detail line: what it is, how big, how old, how
// often it was consecutively copied, and why it is masked.
func entrySubtitle(e Entry, now time.Time) string {
	var parts []string
	switch e.Kind {
	case KindImage:
		parts = append(parts, imageLabel(e.MIME), sizeLabel(e.Size))
	case KindFiles:
		n := len(parseURIList(e.Text))
		if n == 1 {
			parts = append(parts, "1 file")
		} else {
			parts = append(parts, fmt.Sprintf("%d files", n))
		}
	}
	parts = append(parts, ageLabel(now.Sub(e.Time)))
	if e.Copies > 1 {
		parts = append(parts, fmt.Sprintf("×%d", e.Copies))
	}
	if e.Sensitive {
		parts = append(parts, "hidden — "+e.MaskReason)
	}
	return strings.Join(parts, " · ")
}

// entryIcon picks the row icon: the image itself for image entries (the
// launcher scales any absolute Icon.Path to the 24px slot — the Steam
// librarycache precedent), theme icons otherwise.
func entryIcon(e Entry) providers.Icon {
	switch {
	case e.Kind == KindImage:
		return providers.Icon{Path: e.ImagePath}
	case e.Sensitive:
		return providers.Icon{ThemeName: iconSensitive}
	case e.Kind == KindFiles:
		return providers.Icon{ThemeName: iconFiles}
	default:
		return providers.Icon{ThemeName: iconText}
	}
}

// matchText is what the fuzzy filter runs against: the text itself, file
// basenames for a uri-list, and a fixed keyword for images so "clip image"
// narrows to pictures.
func matchText(e Entry) string {
	switch e.Kind {
	case KindImage:
		return "image " + imageLabel(e.MIME)
	case KindFiles:
		paths := parseURIList(e.Text)
		names := make([]string, len(paths))
		for i, p := range paths {
			names[i] = filepath.Base(p)
		}
		return strings.Join(names, " ")
	default:
		return e.Text
	}
}

// parseTrigger recognizes the trigger keyword and splits off the filter.
// The keyword is any prefix of "clipboard" at least as long as "clip" — the
// tool is *named* clipboard, so typing the name out (or stopping anywhere
// past clip) must keep triggering it — plus the short alias "cb". Keyword
// matching is case-insensitive; the remainder keeps its case for the scorer
// (totp's trigger convention).
func parseTrigger(q string) (triggered bool, filter string) {
	q = strings.TrimSpace(q)
	word, rest, _ := strings.Cut(q, " ")
	lower := strings.ToLower(word)
	if lower != "cb" && (len(lower) < len("clip") || !strings.HasPrefix("clipboard", lower)) {
		return false, ""
	}
	return true, strings.TrimSpace(rest)
}

// parseURIList extracts local paths from a text/uri-list payload: one URI per
// line, #-comments skipped (RFC 2483), file:// URIs percent-decoded, anything
// non-file (a remote URL a browser put there) kept verbatim so the row still
// says something meaningful.
func parseURIList(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if u, err := url.Parse(line); err == nil && u.Scheme == "file" {
			if p, err := url.PathUnescape(u.Path); err == nil {
				out = append(out, p)
				continue
			}
			out = append(out, u.Path)
			continue
		}
		out = append(out, line)
	}
	return out
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return strings.TrimSpace(s)
}

// ageLabel renders how long ago an entry was copied, coarsely — the list is
// re-rendered per keystroke, not per tick, so seconds precision would lie.
func ageLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// imageLabel renders an image MIME as its short format name.
func imageLabel(mime string) string {
	switch mime {
	case "image/png":
		return "PNG"
	case "image/jpeg":
		return "JPEG"
	default:
		return strings.TrimPrefix(mime, "image/")
	}
}

// sizeLabel renders a byte count with one coarse unit.
func sizeLabel(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
