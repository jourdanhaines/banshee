package calc

import (
	"context"
	"reflect"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

func TestQueryTrigger(t *testing.T) {
	tests := []struct {
		query   string
		wantRow bool
	}{
		// auto-detect positives
		{query: "2+2", wantRow: true},
		{query: "(1+2)*3", wantRow: true},
		{query: "sqrt(9)", wantRow: true},
		{query: "2*pi", wantRow: true},
		{query: "10%3", wantRow: true},
		{query: "2^10", wantRow: true},
		{query: "2-1", wantRow: true},

		// auto-detect negatives
		{query: "", wantRow: false},
		{query: "   ", wantRow: false},
		{query: "42", wantRow: false},
		{query: "-5", wantRow: false},
		{query: "3.14", wantRow: false},
		{query: "e", wantRow: false},
		{query: "pi", wantRow: false},
		{query: "(5)", wantRow: false},
		{query: "2025-01-01", wantRow: false},
		{query: "2025-1-1", wantRow: false},
		{query: "007", wantRow: false},
		{query: "01+1", wantRow: false},
		{query: "1.2.3", wantRow: false},
		{query: "192.168.1.1", wantRow: false},
		{query: "12:30", wantRow: false},
		{query: "hello", wantRow: false},
		{query: "vim 2", wantRow: false},
		{query: "1/0", wantRow: false},
		{query: "0-1/0", wantRow: false},

		// forced prefixes
		{query: "= 42", wantRow: true},
		{query: "=2+2", wantRow: true},
		{query: "= e", wantRow: true},
		{query: "calc 2+2", wantRow: true},
		{query: "calc 42", wantRow: true},
		{query: "calc", wantRow: false},
		{query: "= ", wantRow: false},
		{query: "= 2++", wantRow: false},
		{query: "calc hello", wantRow: false},
		{query: "= 1/0", wantRow: false},
	}
	p := New()
	for _, tt := range tests {
		name := tt.query
		if name == "" {
			name = "(empty)"
		}
		t.Run(name, func(t *testing.T) {
			res, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query(%q) error: %v", tt.query, err)
			}
			if got := len(res) == 1; got != tt.wantRow {
				t.Errorf("Query(%q) rows = %d, want row: %v", tt.query, len(res), tt.wantRow)
			}
		})
	}
}

func TestQueryResultShape(t *testing.T) {
	res, err := New().Query(context.Background(), "2+2")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Query() rows = %d, want 1", len(res))
	}
	want := providers.Result{
		ID:       "calc:2+2",
		Title:    "4",
		Subtitle: "2+2 = 4 · Enter copies result, Tab copies equation",
		Icon:     providers.Icon{ThemeName: "accessories-calculator-symbolic"},
		Category: providers.CatCalc,
		Score:    Score,
		Action:   providers.Action{Kind: providers.ActClipboardCopy, Text: "4"},
		AltAction: &providers.Action{
			Kind: providers.ActClipboardCopy,
			Text: "2+2 = 4",
		},
	}
	if !reflect.DeepEqual(res[0], want) {
		t.Errorf("Query() = %+v, want %+v", res[0], want)
	}
}

func TestQueryStripsForcedPrefix(t *testing.T) {
	res, err := New().Query(context.Background(), "= 3*3")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Query() rows = %d, want 1", len(res))
	}
	if res[0].ID != "calc:3*3" {
		t.Errorf("ID = %q, want %q", res[0].ID, "calc:3*3")
	}
	if res[0].AltAction.Text != "3*3 = 9" {
		t.Errorf("AltAction.Text = %q, want %q", res[0].AltAction.Text, "3*3 = 9")
	}
}

func TestQueryHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Query(ctx, "2+2"); err != context.Canceled {
		t.Errorf("Query() error = %v, want context.Canceled", err)
	}
}
