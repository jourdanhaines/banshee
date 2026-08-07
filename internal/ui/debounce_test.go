package ui

import (
	"testing"
	"time"
)

// fakeTimer is a hand-driven Timer: scheduled callbacks only run when the test
// calls Advance, which makes the debounce state machine fully deterministic.
type fakeTimer struct {
	pending   []*fakeTimeout
	scheduled int // total Trigger-initiated schedules, including cancelled ones
	cancelled int
}

type fakeTimeout struct {
	delay    time.Duration
	fn       func()
	cancel   bool
	finished bool
}

func (f *fakeTimer) Timer() Timer {
	return func(d time.Duration, fn func()) func() {
		t := &fakeTimeout{delay: d, fn: fn}
		f.pending = append(f.pending, t)
		f.scheduled++
		return func() {
			if !t.finished {
				t.cancel = true
				f.cancelled++
			}
		}
	}
}

// Advance runs every scheduled-and-not-cancelled callback, in order.
func (f *fakeTimer) Advance() {
	pending := f.pending
	f.pending = nil
	for _, t := range pending {
		if t.cancel || t.finished {
			continue
		}
		t.finished = true
		t.fn()
	}
}

func TestDebouncer(t *testing.T) {
	tests := []struct {
		name string
		// run drives the debouncer; it appends to *got via the callbacks it
		// schedules.
		run  func(d *Debouncer, ft *fakeTimer, record func(string))
		want []string
	}{
		{
			name: "single trigger fires once",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("a") })
				ft.Advance()
			},
			want: []string{"a"},
		},
		{
			name: "burst collapses to the last callback",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("b") })
				d.Trigger(func() { rec("bl") })
				d.Trigger(func() { rec("bla") })
				ft.Advance()
			},
			want: []string{"bla"},
		},
		{
			name: "cancel drops the pending callback",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("a") })
				d.Cancel()
				ft.Advance()
			},
			want: nil,
		},
		{
			name: "fire runs immediately and drops pending",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("typed") })
				d.Fire(func() { rec("shown") })
				ft.Advance()
			},
			want: []string{"shown"},
		},
		{
			name: "separate bursts each fire",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("one") })
				ft.Advance()
				d.Trigger(func() { rec("two") })
				ft.Advance()
			},
			want: []string{"one", "two"},
		},
		{
			name: "cancel after firing is a no-op",
			run: func(d *Debouncer, ft *fakeTimer, rec func(string)) {
				d.Trigger(func() { rec("a") })
				ft.Advance()
				d.Cancel()
				ft.Advance()
			},
			want: []string{"a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTimer{}
			d := NewDebouncer(QueryDebounce, ft.Timer())
			var got []string
			tt.run(d, ft, func(s string) { got = append(got, s) })

			if len(got) != len(tt.want) {
				t.Fatalf("callbacks = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("callbacks = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestDebouncerPending(t *testing.T) {
	ft := &fakeTimer{}
	d := NewDebouncer(QueryDebounce, ft.Timer())

	if d.Pending() {
		t.Error("fresh debouncer reports pending")
	}
	d.Trigger(func() {})
	if !d.Pending() {
		t.Error("after Trigger, Pending() = false, want true")
	}
	ft.Advance()
	if d.Pending() {
		t.Error("after the callback ran, Pending() = true, want false")
	}
	d.Trigger(func() {})
	d.Cancel()
	if d.Pending() {
		t.Error("after Cancel, Pending() = true, want false")
	}
}

func TestDebouncerCancelsExactlyOncePerSupersededTrigger(t *testing.T) {
	ft := &fakeTimer{}
	d := NewDebouncer(QueryDebounce, ft.Timer())

	// Five keystrokes: four superseded schedules cancelled, one survives.
	for i := 0; i < 5; i++ {
		d.Trigger(func() {})
	}
	ft.Advance()

	if ft.scheduled != 5 {
		t.Errorf("scheduled = %d, want 5", ft.scheduled)
	}
	if ft.cancelled != 4 {
		t.Errorf("cancelled = %d, want 4", ft.cancelled)
	}
}

// syncTimer runs callbacks the moment they are scheduled — the degenerate case
// that would otherwise leave a stale cancel func behind.
type syncTimer struct{ runs int }

func (s *syncTimer) Timer() Timer {
	return func(_ time.Duration, fn func()) func() {
		s.runs++
		fn()
		return func() {}
	}
}

func TestDebouncerSurvivesSynchronousTimer(t *testing.T) {
	st := &syncTimer{}
	d := NewDebouncer(QueryDebounce, st.Timer())

	var got []string
	d.Trigger(func() { got = append(got, "a") })
	if d.Pending() {
		t.Error("Pending() = true after a synchronous timer already fired")
	}
	d.Trigger(func() { got = append(got, "b") })

	if st.runs != 2 || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("runs = %d, callbacks = %v; want 2 runs of [a b]", st.runs, got)
	}
}

func TestDebouncerReentrantTrigger(t *testing.T) {
	ft := &fakeTimer{}
	d := NewDebouncer(QueryDebounce, ft.Timer())

	var got []string
	d.Trigger(func() {
		got = append(got, "outer")
		d.Trigger(func() { got = append(got, "inner") })
	})
	ft.Advance() // runs "outer", which schedules "inner"
	if !d.Pending() {
		t.Fatal("re-entrant Trigger did not leave a pending callback")
	}
	ft.Advance() // runs "inner"

	if len(got) != 2 || got[0] != "outer" || got[1] != "inner" {
		t.Fatalf("callbacks = %v, want [outer inner]", got)
	}
}

func TestQueryDebounceMatchesPlan(t *testing.T) {
	if QueryDebounce != 30*time.Millisecond {
		t.Errorf("QueryDebounce = %v, want 30ms", QueryDebounce)
	}
}
