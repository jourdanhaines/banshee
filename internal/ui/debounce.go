package ui

import "time"

// QueryDebounce is how long the launcher waits after the last keystroke before
// firing a query. Short enough to feel instant, long enough that a fast typist
// does not fan out one aggregator pass per character.
const QueryDebounce = 30 * time.Millisecond

// Timer schedules fn to run after d and returns a function that cancels the
// pending run. Cancel must be safe to call after the timer has already fired.
//
// This is the seam that keeps debouncing testable: production passes
// glibTimer (a glib main-loop timeout, so fn runs on the GTK thread), tests
// pass a fake clock.
type Timer func(d time.Duration, fn func()) (cancel func())

// Debouncer coalesces a burst of events into a single delayed callback: each
// Trigger cancels the previously scheduled callback and schedules a new one.
//
// A Debouncer is not safe for concurrent use; the launcher drives it from the
// GTK main thread only.
type Debouncer struct {
	delay  time.Duration
	timer  Timer
	cancel func()
}

// NewDebouncer returns a Debouncer that waits delay between the last Trigger
// and the callback, scheduling through timer. A nil timer panics on first use,
// which is what you want — there is no sane silent fallback.
func NewDebouncer(delay time.Duration, timer Timer) *Debouncer {
	return &Debouncer{delay: delay, timer: timer}
}

// Trigger schedules fn to run after the debounce delay, cancelling any
// callback scheduled by an earlier Trigger. Only the most recent fn ever runs.
func (d *Debouncer) Trigger(fn func()) {
	d.Cancel()

	// fired guards against a Timer that runs its callback synchronously (some
	// test doubles do): without it, the assignment below would resurrect a
	// cancel func for a callback that already ran.
	fired := false
	cancel := d.timer(d.delay, func() {
		fired = true
		// Drop the stale cancel before invoking fn, so Pending reports
		// correctly and a re-entrant Trigger from fn behaves.
		d.cancel = nil
		fn()
	})
	if !fired {
		d.cancel = cancel
	}
}

// Fire runs fn immediately, cancelling anything pending. Used for events that
// must not wait out the debounce, such as the empty-query population done on
// every Show.
func (d *Debouncer) Fire(fn func()) {
	d.Cancel()
	fn()
}

// Cancel drops any pending callback. Safe to call when nothing is pending.
func (d *Debouncer) Cancel() {
	if d.cancel != nil {
		c := d.cancel
		d.cancel = nil
		c()
	}
}

// Pending reports whether a callback is scheduled and has not yet run.
func (d *Debouncer) Pending() bool { return d.cancel != nil }
