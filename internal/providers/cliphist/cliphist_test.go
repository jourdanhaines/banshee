package cliphist

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/fuzzy"
	"github.com/jourdanhaines/banshee/internal/providers"
)

// seededProvider returns a provider over a store holding, newest-first:
// a sensitive token, plain text, a file list and an image.
func seededProvider(t *testing.T) (*Provider, *Store) {
	t.Helper()
	nowFn, now := fixedClock()
	s := NewStore(WithClock(nowFn), WithImageDir(t.TempDir()))
	s.Add(KindImage, "image/png", []byte("png-bytes"), false, "")
	*now = now.Add(time.Minute)
	s.Add(KindFiles, "text/uri-list", []byte("file:///home/u/report%20final.pdf\nfile:///home/u/notes.txt\n"), false, "")
	*now = now.Add(time.Minute)
	s.Add(KindText, "text/plain", []byte("hello world"), false, "")
	*now = now.Add(time.Minute)
	s.Add(KindText, "text/plain", []byte("ghp_16C7e42F292c6912E7710c838347Ae178B4a"), true, "GitHub token")
	*now = now.Add(time.Minute)
	p := New(s, fuzzy.Score, WithNow(func() time.Time { return *now }))
	return p, s
}

func TestParseTrigger(t *testing.T) {
	tests := []struct {
		q          string
		wantOn     bool
		wantFilter string
	}{
		{"clip", true, ""},
		{"CB", true, ""},
		{"clip hello", true, "hello"},
		{"cb Report", true, "Report"},
		{"clipboard", true, ""},
		{"Clipboard hello", true, "hello"},
		{"clipbo", true, ""},
		{"clipbo x", true, "x"},
		{"cli", false, ""},
		{"clipx", false, ""},
		{"clipboards", false, ""},
		{"cbx", false, ""},
		{"", false, ""},
		{"open repo", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			on, filter := parseTrigger(tt.q)
			if on != tt.wantOn || filter != tt.wantFilter {
				t.Errorf("parseTrigger(%q) = (%v, %q), want (%v, %q)", tt.q, on, filter, tt.wantOn, tt.wantFilter)
			}
		})
	}
}

func TestQueryUntriggered(t *testing.T) {
	p, _ := seededProvider(t)
	for _, q := range []string{"", "hello", "ghp_16C7e42F292c"} {
		res, err := p.Query(context.Background(), q)
		if err != nil || res != nil {
			t.Errorf("Query(%q) = %v, %v — clipboard must stay hidden untriggered", q, res, err)
		}
	}
}

func TestQueryBareTrigger(t *testing.T) {
	p, _ := seededProvider(t)
	res, err := p.Query(context.Background(), "clip")
	if err != nil {
		t.Fatalf("Query error: %v", err)
	}
	if len(res) != 4 {
		t.Fatalf("len = %d, want 4", len(res))
	}

	// Newest first, scores strictly descending so the aggregator sort keeps
	// recency order.
	for i := 1; i < len(res); i++ {
		if res[i].Score >= res[i-1].Score {
			t.Errorf("scores not descending: %d then %d", res[i-1].Score, res[i].Score)
		}
	}
	if res[0].Score != TriggerScore {
		t.Errorf("newest score = %d, want %d", res[0].Score, TriggerScore)
	}

	// Newest is the sensitive token: masked title, no raw content anywhere.
	if res[0].Title != "ghp•••••" {
		t.Errorf("masked title = %q", res[0].Title)
	}
	if !strings.Contains(res[0].Subtitle, "hidden — GitHub token") {
		t.Errorf("masked subtitle = %q", res[0].Subtitle)
	}
	if res[0].Icon.ThemeName != iconSensitive {
		t.Errorf("masked icon = %+v", res[0].Icon)
	}
	for _, r := range res {
		if strings.Contains(r.Title, "ghp_") || strings.Contains(r.Subtitle, "ghp_") {
			t.Errorf("raw secret leaked into row: %q / %q", r.Title, r.Subtitle)
		}
	}

	if res[1].Title != "hello world" || !strings.Contains(res[1].Subtitle, "ago") {
		t.Errorf("text row = %q / %q", res[1].Title, res[1].Subtitle)
	}
	if res[2].Title != "report final.pdf +1 more" || !strings.Contains(res[2].Subtitle, "2 files") {
		t.Errorf("files row = %q / %q", res[2].Title, res[2].Subtitle)
	}
	if res[3].Title != "Copied image" || !strings.Contains(res[3].Subtitle, "PNG") {
		t.Errorf("image row = %q / %q", res[3].Title, res[3].Subtitle)
	}
	if res[3].Icon.Path == "" {
		t.Errorf("image row icon = %+v, want thumbnail path", res[3].Icon)
	}

	// Every row: category, copy action, delete alt-action, ID-only payloads.
	for _, r := range res {
		if r.Category != providers.CatClipboard {
			t.Errorf("category = %v", r.Category)
		}
		if r.Action.Kind != ActClipCopy || len(r.Action.Argv) != 1 {
			t.Errorf("action = %+v", r.Action)
		}
		if r.AltAction == nil || r.AltAction.Kind != ActClipDelete {
			t.Errorf("alt action = %+v", r.AltAction)
		}
	}
}

func TestQueryFiltered(t *testing.T) {
	p, _ := seededProvider(t)

	t.Run("matches file basenames", func(t *testing.T) {
		res, err := p.Query(context.Background(), "clip report")
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || !strings.HasPrefix(res[0].Title, "report final.pdf") {
			t.Errorf("res = %+v", res)
		}
	})

	t.Run("matches text content", func(t *testing.T) {
		res, err := p.Query(context.Background(), "cb hello")
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].Title != "hello world" {
			t.Errorf("res = %+v", res)
		}
	})

	t.Run("sensitive entries never match a filter", func(t *testing.T) {
		// The raw token would fuzzy-match itself; the masked row must not.
		res, err := p.Query(context.Background(), "clip ghp")
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if strings.Contains(r.Title, "•") {
				t.Errorf("sensitive row matched a filter: %+v", r)
			}
		}
	})

	t.Run("image keyword", func(t *testing.T) {
		res, err := p.Query(context.Background(), "clip image")
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 1 || res[0].Title != "Copied image" {
			t.Errorf("res = %+v", res)
		}
	})
}

func TestQueryCancelled(t *testing.T) {
	p, _ := seededProvider(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "clip"); err == nil {
		t.Error("Query on cancelled ctx did not error")
	}
}

func TestParseURIList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "file URIs percent-decoded",
			in:   "file:///home/u/report%20final.pdf\nfile:///home/u/b.txt\n",
			want: []string{"/home/u/report final.pdf", "/home/u/b.txt"},
		},
		{
			name: "comments and blanks skipped",
			in:   "# copied from nautilus\n\nfile:///a\n",
			want: []string{"/a"},
		},
		{
			name: "non-file URI kept verbatim",
			in:   "https://example.com/x\n",
			want: []string{"https://example.com/x"},
		},
		{
			name: "empty",
			in:   "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseURIList(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseURIList() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSubtitleShapes(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		e    Entry
		want string
	}{
		{
			name: "repeat copies counted",
			e:    Entry{Kind: KindText, Copies: 3, Time: base.Add(-30 * time.Second)},
			want: "just now · ×3",
		},
		{
			name: "image kind and size lead",
			e:    Entry{Kind: KindImage, MIME: "image/png", Size: 300 << 10, Copies: 1, Time: base.Add(-2 * time.Hour)},
			want: "PNG · 300 KiB · 2h ago",
		},
		{
			name: "old entry in days",
			e:    Entry{Kind: KindText, Copies: 1, Time: base.Add(-49 * time.Hour)},
			want: "2d ago",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := entrySubtitle(tt.e, base); got != tt.want {
				t.Errorf("entrySubtitle() = %q, want %q", got, tt.want)
			}
		})
	}
}
