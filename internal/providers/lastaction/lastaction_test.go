package lastaction

import (
	"context"
	"errors"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/state"
)

type stubReader struct {
	act state.Action
	err error
}

func (s stubReader) Read() (state.Action, error) { return s.act, s.err }

func TestQuery(t *testing.T) {
	tests := []struct {
		name      string
		store     Reader
		query     string
		wantLen   int
		wantTitle string
		wantArgv  []string
	}{
		{
			name:      "target",
			store:     stubReader{act: state.Action{Kind: state.KindTarget, Name: "blacksheep"}},
			wantLen:   1,
			wantTitle: "Resume blacksheep",
			wantArgv:  []string{"banshee", "blacksheep"},
		},
		{
			name:      "group",
			store:     stubReader{act: state.Action{Kind: state.KindGroup, Name: "work"}},
			wantLen:   1,
			wantTitle: "Resume group work",
			wantArgv:  []string{"banshee", "-g", "work"},
		},
		{
			name:    "non-empty query emits nothing",
			store:   stubReader{act: state.Action{Kind: state.KindTarget, Name: "blacksheep"}},
			query:   "black",
			wantLen: 0,
		},
		{
			name:    "no recorded action",
			store:   stubReader{err: state.ErrNoAction},
			wantLen: 0,
		},
		{
			name:    "unreadable state file is not an error",
			store:   stubReader{err: errors.New("boom")},
			wantLen: 0,
		},
		{
			name:    "empty name",
			store:   stubReader{act: state.Action{Kind: state.KindTarget}},
			wantLen: 0,
		},
		{
			name:    "unknown kind",
			store:   stubReader{act: state.Action{Kind: "wat", Name: "x"}},
			wantLen: 0,
		},
		{
			name:    "nil store",
			store:   nil,
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := New(tt.store)
			got, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != tt.wantLen {
				t.Fatalf("got %d results, want %d: %+v", len(got), tt.wantLen, got)
			}
			if tt.wantLen == 0 {
				return
			}
			r := got[0]
			if r.Title != tt.wantTitle {
				t.Errorf("Title = %q, want %q", r.Title, tt.wantTitle)
			}
			if r.Category != providers.CatSession {
				t.Errorf("Category = %v, want CatSession", r.Category)
			}
			if r.Score != Score {
				t.Errorf("Score = %d, want %d", r.Score, Score)
			}
			if r.Action.Kind != providers.ActTerminal {
				t.Errorf("Action.Kind = %q, want %q", r.Action.Kind, providers.ActTerminal)
			}
			if len(r.Action.Argv) != len(tt.wantArgv) {
				t.Fatalf("Argv = %v, want %v", r.Action.Argv, tt.wantArgv)
			}
			for i := range tt.wantArgv {
				if r.Action.Argv[i] != tt.wantArgv[i] {
					t.Fatalf("Argv = %v, want %v", r.Action.Argv, tt.wantArgv)
				}
			}
		})
	}
}

func TestQueryCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := New(stubReader{act: state.Action{Kind: state.KindTarget, Name: "x"}})
	if _, err := p.Query(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestBinaryOverride(t *testing.T) {
	p := New(stubReader{act: state.Action{Kind: state.KindTarget, Name: "x"}})
	p.Binary = "/usr/local/bin/banshee"
	got, err := p.Query(context.Background(), "")
	if err != nil || len(got) != 1 {
		t.Fatalf("Query: %v %v", got, err)
	}
	if got[0].Action.Argv[0] != "/usr/local/bin/banshee" {
		t.Fatalf("Argv[0] = %q", got[0].Action.Argv[0])
	}
}

// Provider must satisfy the frozen interface.
var _ providers.Provider = (*Provider)(nil)
