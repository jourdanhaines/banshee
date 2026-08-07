package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// fakeProvider returns a canned slice (or error) and records the query it saw.
type fakeProvider struct {
	name    string
	results []Result
	err     error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Query(ctx context.Context, q string) ([]Result, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// blockingProvider blocks until ctx is cancelled, then reports the reason.
type blockingProvider struct {
	name    string
	entered chan struct{}
	once    bool
}

func (b *blockingProvider) Name() string { return b.name }

func (b *blockingProvider) Query(ctx context.Context, q string) ([]Result, error) {
	if !b.once {
		b.once = true
		close(b.entered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func res(title string, cat Category, score int) Result {
	return Result{ID: title, Title: title, Category: cat, Score: score}
}

func titles(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Title
	}
	return out
}

func newAgg(t *testing.T, max int, provs ...Provider) (*ConcurrentAggregator, *bytes.Buffer) {
	t.Helper()
	reg := NewRegistry()
	for _, p := range provs {
		reg.Register(p)
	}
	a := NewAggregator(reg, max)
	var buf bytes.Buffer
	a.Logger = log.New(&buf, "", 0)
	return a, &buf
}

// TestQueryBlockOrder is the plan's worked example: "blacksh" matches the
// blacksheep repo, and the four repo-derived providers — which independently
// scored the same repo name and therefore share one score — must come back as
// one contiguous, category-ordered block above everything weaker.
func TestQueryBlockOrder(t *testing.T) {
	const repoScore = 540

	sessionsP := &fakeProvider{name: "sessions", results: []Result{
		res("Open blacksheep session", CatSession, repoScore),
	}}
	githubP := &fakeProvider{name: "github", results: []Result{
		res("Open blacksheep on GitHub", CatGitHub, repoScore),
	}}
	connectorP := &fakeProvider{name: "connectors", results: []Result{
		res("Open blacksheep on Railway", CatConnector, repoScore),
	}}
	reposP := &fakeProvider{name: "repos", results: []Result{
		res("Open blacksheep directory", CatDirectory, repoScore),
	}}
	appsP := &fakeProvider{name: "apps", results: []Result{
		res("Black Sheep Player", CatApp, 40),
	}}
	procsP := &fakeProvider{name: "procs", results: []Result{
		res("Kill blackshd", CatKill, 20),
	}}

	// Registration order deliberately does NOT match expected output order.
	a, _ := newAgg(t, 30, procsP, appsP, reposP, connectorP, githubP, sessionsP)

	got := titles(a.Query(context.Background(), "blacksh"))
	want := []string{
		"Open blacksheep session",
		"Open blacksheep on GitHub",
		"Open blacksheep on Railway",
		"Open blacksheep directory",
		"Black Sheep Player",
		"Kill blackshd",
	}
	if !equal(got, want) {
		t.Fatalf("order =\n  %v\nwant\n  %v", got, want)
	}
}

func TestQuerySorting(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		results []Result
		want    []string
	}{
		{
			name:  "score dominates category",
			query: "x",
			results: []Result{
				res("low-session", CatSession, 10),
				res("high-app", CatApp, 900),
			},
			want: []string{"high-app", "low-session"},
		},
		{
			name:  "category breaks score ties",
			query: "x",
			results: []Result{
				res("dir", CatDirectory, 100),
				res("kill", CatKill, 100),
				res("session", CatSession, 100),
				res("github", CatGitHub, 100),
			},
			want: []string{"session", "github", "dir", "kill"},
		},
		{
			name:  "title breaks category ties",
			query: "x",
			results: []Result{
				res("charlie", CatApp, 100),
				res("alpha", CatApp, 100),
				res("bravo", CatApp, 100),
			},
			want: []string{"alpha", "bravo", "charlie"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newAgg(t, 30, &fakeProvider{name: "f", results: tt.results})
			if got := titles(a.Query(context.Background(), tt.query)); !equal(got, tt.want) {
				t.Fatalf("order = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryThreshold(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		minScore int
		results  []Result
		want     []string
	}{
		{
			name:     "weak app dropped on non-empty query",
			query:    "q",
			minScore: DefaultMinScore,
			results: []Result{
				res("strong app", CatApp, 5),
				res("weak app", CatApp, 0),
			},
			want: []string{"strong app"},
		},
		{
			name:     "negative score dropped",
			query:    "q",
			minScore: DefaultMinScore,
			results:  []Result{res("noise", CatKill, -9)},
			want:     nil,
		},
		{
			name:     "repo-derived categories never thresholded",
			query:    "q",
			minScore: DefaultMinScore,
			results: []Result{
				res("session", CatSession, -100),
				res("github", CatGitHub, 0),
				res("connector", CatConnector, 0),
				res("dir", CatDirectory, 0),
			},
			// All four survive despite scores at/below the cutoff; the
			// deeply negative session row simply sorts last.
			want: []string{"github", "connector", "dir", "session"},
		},
		{
			name:     "boundary score survives",
			query:    "q",
			minScore: DefaultMinScore,
			results:  []Result{res("edge", CatApp, DefaultMinScore)},
			want:     []string{"edge"},
		},
		{
			name:     "empty query bypasses threshold",
			query:    "",
			minScore: DefaultMinScore,
			results: []Result{
				res("default app", CatApp, 0),
				res("default proc", CatKill, 0),
				res("default plugin", CatPlugin, 0),
			},
			want: []string{"default app", "default proc", "default plugin"},
		},
		{
			name:     "custom higher threshold",
			query:    "q",
			minScore: 100,
			results: []Result{
				res("keep", CatApp, 100),
				res("drop", CatApp, 99),
			},
			want: []string{"keep"},
		},
		{
			name:     "plugin results thresholded",
			query:    "q",
			minScore: DefaultMinScore,
			results: []Result{
				res("good plugin", CatPlugin, 3),
				res("bad plugin", CatPlugin, -1),
			},
			want: []string{"good plugin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newAgg(t, 30, &fakeProvider{name: "f", results: tt.results})
			a.MinScore = tt.minScore
			got := titles(a.Query(context.Background(), tt.query))
			if !equal(got, tt.want) {
				t.Fatalf("results = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQueryCap(t *testing.T) {
	var results []Result
	for i := 0; i < 50; i++ {
		results = append(results, res(string(rune('a'+i%26))+"-item", CatApp, 100-i))
	}
	a, _ := newAgg(t, 7, &fakeProvider{name: "f", results: results})
	if got := a.Query(context.Background(), "q"); len(got) != 7 {
		t.Fatalf("len = %d, want 7", len(got))
	}
}

func TestNewAggregatorDefaultsMax(t *testing.T) {
	a := NewAggregator(NewRegistry(), 0)
	if a.MaxResults != DefaultMaxResults {
		t.Fatalf("MaxResults = %d, want %d", a.MaxResults, DefaultMaxResults)
	}
	if a.MinScore != DefaultMinScore {
		t.Fatalf("MinScore = %d, want %d", a.MinScore, DefaultMinScore)
	}
	if a.Logger == nil {
		t.Fatal("Logger must not be nil")
	}
}

// TestQueryProviderErrorIsDropped proves one broken provider cannot blank the
// launcher: its results vanish, everyone else's survive, and the failure is
// logged.
func TestQueryProviderErrorIsDropped(t *testing.T) {
	good := &fakeProvider{name: "good", results: []Result{res("kept", CatSession, 10)}}
	bad := &fakeProvider{name: "bad", err: errors.New("boom")}

	a, logBuf := newAgg(t, 30, bad, good)
	got := titles(a.Query(context.Background(), "q"))
	if !equal(got, []string{"kept"}) {
		t.Fatalf("results = %v, want [kept]", got)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "bad") || !strings.Contains(logged, "boom") {
		t.Fatalf("log = %q, want provider name and error", logged)
	}
}

func TestQueryAllProvidersFail(t *testing.T) {
	a, _ := newAgg(t, 30,
		&fakeProvider{name: "a", err: errors.New("x")},
		&fakeProvider{name: "b", err: errors.New("y")},
	)
	if got := a.Query(context.Background(), "q"); len(got) != 0 {
		t.Fatalf("results = %v, want empty", got)
	}
}

func TestQueryEmptyRegistry(t *testing.T) {
	a := NewAggregator(NewRegistry(), 30)
	if got := a.Query(context.Background(), "q"); got != nil {
		t.Fatalf("results = %v, want nil", got)
	}
}

// TestQueryCancellation proves a provider that parks on ctx.Done unblocks as
// soon as the caller cancels — the per-keystroke supersede path.
func TestQueryCancellation(t *testing.T) {
	blocker := &blockingProvider{name: "blocker", entered: make(chan struct{})}
	fast := &fakeProvider{name: "fast", results: []Result{res("fast", CatSession, 10)}}
	a, _ := newAgg(t, 30, blocker, fast)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []Result, 1)
	go func() { done <- a.Query(ctx, "q") }()

	select {
	case <-blocker.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never ran")
	}

	select {
	case <-done:
		t.Fatal("Query returned before cancellation")
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case got := <-done:
		// The blocker contributed nothing; the fast provider's row survives.
		if !equal(titles(got), []string{"fast"}) {
			t.Fatalf("results = %v, want [fast]", titles(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Query did not return promptly after cancellation")
	}
}

// TestQuerySharesOneContext proves every provider sees a context derived from
// the caller's, so a single cancel reaches all of them.
func TestQuerySharesOneContext(t *testing.T) {
	b1 := &blockingProvider{name: "b1", entered: make(chan struct{})}
	b2 := &blockingProvider{name: "b2", entered: make(chan struct{})}
	a, _ := newAgg(t, 30, b1, b2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Query(ctx, "q"); close(done) }()

	for _, ch := range []chan struct{}{b1.entered, b2.entered} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("provider never ran")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shared context did not cancel all providers")
	}
}

func TestQueryNilAggregator(t *testing.T) {
	var a *ConcurrentAggregator
	if got := a.Query(context.Background(), "q"); got != nil {
		t.Fatalf("results = %v, want nil", got)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestQueryCancellationIsNotLogged pins the log's signal-to-noise ratio. The UI
// cancels the previous query generation on every debounced keystroke and never
// joins it, so the superseded pass is almost always still in flight — treating
// each provider's ctx.Err() as a failure wrote ~7 lines per keystroke into
// ~/.local/state/banshee/daemon.log and buried the real provider errors.
func TestQueryCancellationIsNotLogged(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"context canceled", context.Canceled},
		{"context deadline exceeded", context.DeadlineExceeded},
		{"wrapped cancellation", fmt.Errorf("provider gave up: %w", context.Canceled)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logBuf := newAgg(t, 30, &fakeProvider{name: "cancelled", err: tt.err})
			a.Query(context.Background(), "q")
			if logged := logBuf.String(); logged != "" {
				t.Fatalf("log = %q, want nothing for a cancellation", logged)
			}
		})
	}
}

// TestSetLimitsAreObserved covers the reload path: `banshee reload` changes
// max_results on the GTK main loop while query goroutines are reading it, so
// the assignment has to go through the setter.
func TestSetLimitsAreObserved(t *testing.T) {
	var results []Result
	for i := 0; i < 10; i++ {
		results = append(results, res(string(rune('a'+i))+"-item", CatSession, 100-i))
	}
	a, _ := newAgg(t, 30, &fakeProvider{name: "f", results: results})

	a.SetMaxResults(3)
	if got := a.Query(context.Background(), "q"); len(got) != 3 {
		t.Fatalf("len = %d, want 3 after SetMaxResults", len(got))
	}
	a.SetMinScore(200)
	a.SetMaxResults(30)
	if got := a.Query(context.Background(), "q"); len(got) != 10 {
		t.Fatalf("len = %d: repo-derived categories are never thresholded", len(got))
	}
}

// TestQueryIsRaceFreeAgainstReload runs the exact interleaving `go test -race`
// used to flag: a live query while the tunables are being reconfigured.
func TestQueryIsRaceFreeAgainstReload(t *testing.T) {
	a, _ := newAgg(t, 30, &fakeProvider{name: "f", results: []Result{res("x", CatSession, 1)}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			a.SetMaxResults(i%20 + 1)
			a.SetMinScore(i % 3)
		}
	}()
	for i := 0; i < 200; i++ {
		a.Query(context.Background(), "q")
	}
	<-done
}
