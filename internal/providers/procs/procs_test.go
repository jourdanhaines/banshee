package procs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// substringScorer is a deterministic stand-in for internal/fuzzy.Score.
func substringScorer(query, candidate string) (int, bool) {
	q, c := strings.ToLower(query), strings.ToLower(candidate)
	i := strings.Index(c, q)
	if i < 0 {
		return 0, false
	}
	return 100 - i - (len(c) - len(q)), true
}

// fakeProc is one entry in a fake procfs tree.
type fakeProc struct {
	pid  int
	comm string
	argv []string // empty argv writes an empty cmdline, i.e. a kernel thread
}

// writeProcTree builds a fake /proc under a temp dir. cmdline entries are
// NUL-separated and NUL-terminated exactly like the kernel writes them.
func writeProcTree(t *testing.T, procs []fakeProc, extraDirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range procs {
		dir := filepath.Join(root, strconv.Itoa(p.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(p.comm+"\n"), 0o644); err != nil {
			t.Fatalf("write comm: %v", err)
		}
		var cmdline string
		for _, a := range p.argv {
			cmdline += a + "\x00"
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
			t.Fatalf("write cmdline: %v", err)
		}
	}
	for _, d := range extraDirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	return root
}

func sampleProcs() []fakeProc {
	return []fakeProc{
		{pid: 2, comm: "kthreadd"}, // kernel thread: no cmdline
		{pid: 101, comm: "firefox", argv: []string{"/usr/lib/firefox/firefox"}}, // group of 3
		{pid: 300, comm: "firefox", argv: []string{"/usr/lib/firefox/firefox", "-c"}},
		{pid: 42, comm: "firefox", argv: []string{"/usr/lib/firefox/firefox", "-b"}},
		{pid: 500, comm: "slack", argv: []string{"/usr/bin/slack", "--no-sandbox"}},
		{pid: 700, comm: "kitty", argv: []string{"kitty"}},
		{pid: 900, comm: "banshee", argv: []string{"banshee", "daemon"}}, // "self"
	}
}

func newTestProvider(t *testing.T, procs []fakeProc, opts ...Option) *Provider {
	t.Helper()
	root := writeProcTree(t, procs, "self", "sys", "1000-notapid")
	opts = append([]Option{WithRoot(root), WithSelfPID(900)}, opts...)
	return New(substringScorer, opts...)
}

func TestScanGroupsAndFilters(t *testing.T) {
	p := newTestProvider(t, sampleProcs())
	got, err := p.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []Group{
		{Name: "firefox", PIDs: []int{42, 101, 300}},
		{Name: "kitty", PIDs: []int{700}},
		{Name: "slack", PIDs: []int{500}},
	}
	if len(got) != len(want) {
		t.Fatalf("groups = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Name != want[i].Name || !reflect.DeepEqual(got[i].PIDs, want[i].PIDs) {
			t.Fatalf("group %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// The cmdline snippet comes from the first PID encountered in directory
	// order, which is lexical: "101" sorts before "300" and "42".
	if got[0].Cmdline != "/usr/lib/firefox/firefox" {
		t.Fatalf("cmdline = %q, want the first-seen process cmdline", got[0].Cmdline)
	}
}

func TestQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		opts  []Option
		want  []string // result titles, in order
	}{
		{"empty query returns nothing", "", nil, nil},
		{"whitespace query returns nothing", "  ", nil, nil},
		{"matches process name", "fire", nil, []string{"Kill firefox"}},
		{"no match", "zzz", nil, nil},
		{"multiple matches sorted by score", "k", nil, []string{"Kill kitty", "Kill slack"}},
		{"max results caps output", "k", []Option{WithMaxResults(1)}, []string{"Kill kitty"}},
		{"min score filters weak matches", "k", []Option{WithMinScore(95)}, []string{"Kill kitty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newTestProvider(t, sampleProcs(), tt.opts...)
			got, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			var titles []string
			for _, r := range got {
				titles = append(titles, r.Title)
			}
			if !reflect.DeepEqual(titles, tt.want) {
				t.Fatalf("titles = %v, want %v", titles, tt.want)
			}
		})
	}
}

func TestQuerySkipsSelf(t *testing.T) {
	p := newTestProvider(t, sampleProcs())
	got, err := p.Query(context.Background(), "banshee")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected own process to be skipped, got %+v", got)
	}
}

func TestQueryHonorsContextCancellation(t *testing.T) {
	p := newTestProvider(t, sampleProcs())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "fire"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestResultShape(t *testing.T) {
	tests := []struct {
		name         string
		group        Group
		wantSubtitle string
		wantArgv     []string
	}{
		{
			name:         "single process",
			group:        Group{Name: "slack", PIDs: []int{500}, Cmdline: "/usr/bin/slack --no-sandbox"},
			wantSubtitle: "pid 500 · /usr/bin/slack --no-sandbox",
			wantArgv:     []string{"500"},
		},
		{
			name:         "process group",
			group:        Group{Name: "firefox", PIDs: []int{42, 101, 300}, Cmdline: "/usr/lib/firefox/firefox"},
			wantSubtitle: "3 processes · pids 42, 101, 300 · /usr/lib/firefox/firefox",
			wantArgv:     []string{"42", "101", "300"},
		},
		{
			name:         "long pid list is truncated in the subtitle only",
			group:        Group{Name: "chrome", PIDs: []int{1, 2, 3, 4, 5, 6, 7, 8}},
			wantSubtitle: "8 processes · pids 1, 2, 3, 4, 5, 6 +2 more",
			wantArgv:     []string{"1", "2", "3", "4", "5", "6", "7", "8"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Result(tt.group, 7)
			if r.ID != "kill:"+tt.group.Name {
				t.Fatalf("ID = %q", r.ID)
			}
			if r.Title != "Kill "+tt.group.Name {
				t.Fatalf("Title = %q", r.Title)
			}
			if r.Subtitle != tt.wantSubtitle {
				t.Fatalf("Subtitle = %q, want %q", r.Subtitle, tt.wantSubtitle)
			}
			if r.Category != providers.CatKill || r.Score != 7 {
				t.Fatalf("Category/Score = %v/%d", r.Category, r.Score)
			}
			wantAction := providers.Action{Kind: ActKillProcs, Argv: tt.wantArgv, Sig: syscall.SIGTERM}
			if !reflect.DeepEqual(r.Action, wantAction) {
				t.Fatalf("Action = %+v, want %+v", r.Action, wantAction)
			}
			if r.AltAction == nil {
				t.Fatal("AltAction = nil, want SIGKILL action")
			}
			wantAlt := providers.Action{Kind: ActKillProcs, Argv: tt.wantArgv, Sig: syscall.SIGKILL}
			if !reflect.DeepEqual(*r.AltAction, wantAlt) {
				t.Fatalf("AltAction = %+v, want %+v", *r.AltAction, wantAlt)
			}
		})
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name  string
		comm  string
		argv0 string
		want  string
	}{
		{"short comm kept", "slack", "/usr/bin/slack", "slack"},
		{"truncated comm expanded from argv0", "NetworkManagerX", "/usr/bin/NetworkManagerXYZ", "NetworkManagerXYZ"},
		{"truncated comm with unrelated argv0 kept", "systemd-journal", "/usr/bin/other", "systemd-journal"},
		{"exactly 15 chars with matching argv0", "gnome-shell-cal", "/usr/bin/gnome-shell-calendar", "gnome-shell-calendar"},
		{"empty argv0", "abcdefghijklmno", "", "abcdefghijklmno"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayName(tt.comm, tt.argv0); got != tt.want {
				t.Fatalf("displayName(%q, %q) = %q, want %q", tt.comm, tt.argv0, got, tt.want)
			}
		})
	}
}

func TestSnippetTruncation(t *testing.T) {
	long := strings.Repeat("x", snippetLen+20)
	got := snippet([]string{long})
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet not truncated: %q", got)
	}
	if n := len([]rune(got)); n != snippetLen+1 {
		t.Fatalf("snippet rune length = %d, want %d", n, snippetLen+1)
	}
	if got := snippet([]string{"a", "b", "c"}); got != "a b c" {
		t.Fatalf("snippet = %q, want %q", got, "a b c")
	}
}

func TestPidOf(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"1", 1, true},
		{"12345", 12345, true},
		{"self", 0, false},
		{"", 0, false},
		{"12a", 0, false},
		{"0", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, ok := pidOf(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("pidOf(%q) = %d,%v want %d,%v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestScanMissingRoot(t *testing.T) {
	p := New(substringScorer, WithRoot(filepath.Join(t.TempDir(), "nope")))
	if _, err := p.Query(context.Background(), "x"); err == nil {
		t.Fatal("expected error for missing procfs root")
	}
}

func TestProviderImplementsInterface(t *testing.T) {
	var p providers.Provider = newTestProvider(t, sampleProcs())
	if p.Name() != "procs" {
		t.Fatalf("Name = %q, want procs", p.Name())
	}
}
