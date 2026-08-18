package notify

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// call records one fake Notify invocation.
type call struct {
	replacesID uint32
	summary    string
	body       string
	icon       string
	actions    []string
	hints      map[string]dbus.Variant
	expire     int32
}

// fakeBus is a Conn backed by memory: it records calls, assigns ids and lets
// tests inject signals.
type fakeBus struct {
	mu     sync.Mutex
	calls  []call
	nextID uint32
	sigs   chan Signal
	closed bool
}

func newFakeBus() *fakeBus {
	return &fakeBus{sigs: make(chan Signal, 8)}
}

func (f *fakeBus) conn() *Conn {
	return &Conn{
		Notify: func(replacesID uint32, summary, body, icon string,
			actions []string, hints map[string]dbus.Variant, expire int32) (uint32, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calls = append(f.calls, call{replacesID, summary, body, icon, actions, hints, expire})
			if replacesID != 0 {
				return replacesID, nil
			}
			f.nextID++
			return f.nextID, nil
		},
		Signals: f.sigs,
		Close: func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.closed {
				f.closed = true
				close(f.sigs)
			}
		},
	}
}

func (f *fakeBus) last(t *testing.T) call {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no Notify calls recorded")
	}
	return f.calls[len(f.calls)-1]
}

func urgencyOf(t *testing.T, c call) byte {
	t.Helper()
	v, ok := c.hints["urgency"]
	if !ok {
		t.Fatal("no urgency hint")
	}
	b, ok := v.Value().(byte)
	if !ok {
		t.Fatalf("urgency hint is %T, want byte", v.Value())
	}
	return b
}

func TestSendWireMapping(t *testing.T) {
	tests := []struct {
		name         string
		req          Request
		wantExpire   int32
		wantUrgency  byte
		wantResident bool
	}{
		{
			name:        "defaults",
			req:         Request{Summary: "s"},
			wantExpire:  -1,
			wantUrgency: 1,
		},
		{
			name:         "require input",
			req:          Request{Summary: "s", RequireInput: true},
			wantExpire:   0,
			wantUrgency:  2,
			wantResident: true,
		},
		{
			name:         "require input keeps explicit urgency",
			req:          Request{Summary: "s", RequireInput: true, Urgency: UrgencyLow},
			wantExpire:   0,
			wantUrgency:  0,
			wantResident: true,
		},
		{
			name:        "timeout",
			req:         Request{Summary: "s", TimeoutMS: 5000},
			wantExpire:  5000,
			wantUrgency: 1,
		},
		{
			name:         "require input beats timeout",
			req:          Request{Summary: "s", RequireInput: true, TimeoutMS: 5000},
			wantExpire:   0,
			wantUrgency:  2,
			wantResident: true,
		},
		{
			name:        "explicit critical without require input",
			req:         Request{Summary: "s", Urgency: UrgencyCritical},
			wantExpire:  -1,
			wantUrgency: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := newFakeBus()
			n := New(Options{Connect: func() (*Conn, error) { return bus.conn(), nil }})
			defer n.Close()
			if err := n.Send(tt.req); err != nil {
				t.Fatalf("Send: %v", err)
			}
			c := bus.last(t)
			if c.expire != tt.wantExpire {
				t.Errorf("expire = %d, want %d", c.expire, tt.wantExpire)
			}
			if got := urgencyOf(t, c); got != tt.wantUrgency {
				t.Errorf("urgency = %d, want %d", got, tt.wantUrgency)
			}
			_, resident := c.hints["resident"]
			if resident != tt.wantResident {
				t.Errorf("resident hint present = %v, want %v", resident, tt.wantResident)
			}
		})
	}
}

func TestSendActionsAndFields(t *testing.T) {
	bus := newFakeBus()
	n := New(Options{Connect: func() (*Conn, error) { return bus.conn(), nil }})
	defer n.Close()
	err := n.Send(Request{
		Summary: "sum", Body: "body", Icon: "dialog-question-symbolic",
		Actions: []Action{{Key: "default", Label: "Focus"}, {Key: "x", Label: "X"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	c := bus.last(t)
	if c.summary != "sum" || c.body != "body" || c.icon != "dialog-question-symbolic" {
		t.Errorf("fields = %q %q %q", c.summary, c.body, c.icon)
	}
	want := []string{"default", "Focus", "x", "X"}
	if len(c.actions) != len(want) {
		t.Fatalf("actions = %v, want %v", c.actions, want)
	}
	for i := range want {
		if c.actions[i] != want[i] {
			t.Fatalf("actions = %v, want %v", c.actions, want)
		}
	}
}

func TestKeyReplacement(t *testing.T) {
	bus := newFakeBus()
	n := New(Options{Connect: func() (*Conn, error) { return bus.conn(), nil }})
	defer n.Close()

	if err := n.Send(Request{Key: "a", Summary: "first"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	first := bus.last(t)
	if first.replacesID != 0 {
		t.Fatalf("first replacesID = %d, want 0", first.replacesID)
	}
	if err := n.Send(Request{Key: "a", Summary: "second"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := bus.last(t).replacesID; got != 1 {
		t.Errorf("second replacesID = %d, want 1", got)
	}
	// A different key starts fresh.
	if err := n.Send(Request{Key: "b", Summary: "other"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := bus.last(t).replacesID; got != 0 {
		t.Errorf("other-key replacesID = %d, want 0", got)
	}
}

func TestEventDispatchAndCleanup(t *testing.T) {
	bus := newFakeBus()
	n := New(Options{Connect: func() (*Conn, error) { return bus.conn(), nil }})
	defer n.Close()

	events := make(chan Event, 4)
	err := n.Send(Request{Key: "a", Summary: "s", OnEvent: func(e Event) { events <- e }})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	bus.sigs <- Signal{ID: 1, ActionKey: "default"}
	select {
	case e := <-events:
		if e.ActionKey != "default" || e.Closed {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no action event delivered")
	}

	bus.sigs <- Signal{ID: 1, Closed: true, Reason: 2}
	select {
	case e := <-events:
		if !e.Closed || e.Reason != 2 {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("no close event delivered")
	}

	// The close dropped the handler and the key mapping: a signal for the old
	// id goes nowhere, and the key starts a fresh notification.
	bus.sigs <- Signal{ID: 1, ActionKey: "default"}
	if err := n.Send(Request{Key: "a", Summary: "again"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := bus.last(t).replacesID; got != 0 {
		t.Errorf("replacesID after close = %d, want 0", got)
	}
	select {
	case e := <-events:
		t.Fatalf("stale handler fired: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestConnectFailureAndRedial(t *testing.T) {
	dials := 0
	bus := newFakeBus()
	n := New(Options{Connect: func() (*Conn, error) {
		dials++
		if dials == 1 {
			return nil, errors.New("no bus")
		}
		return bus.conn(), nil
	}})
	defer n.Close()

	if err := n.Send(Request{Summary: "s"}); err == nil {
		t.Fatal("Send succeeded with no bus")
	}
	if err := n.Send(Request{Summary: "s"}); err != nil {
		t.Fatalf("Send after redial: %v", err)
	}
	if dials != 2 {
		t.Errorf("dials = %d, want 2", dials)
	}
}

func TestCallFailureDropsConn(t *testing.T) {
	dials := 0
	broken := &Conn{
		Notify: func(uint32, string, string, string, []string, map[string]dbus.Variant, int32) (uint32, error) {
			return 0, errors.New("daemon gone")
		},
		Signals: make(chan Signal),
		Close:   func() {},
	}
	bus := newFakeBus()
	n := New(Options{Connect: func() (*Conn, error) {
		dials++
		if dials == 1 {
			return broken, nil
		}
		return bus.conn(), nil
	}})
	defer n.Close()

	if err := n.Send(Request{Summary: "s"}); err == nil {
		t.Fatal("Send succeeded on broken conn")
	}
	if err := n.Send(Request{Summary: "s"}); err != nil {
		t.Fatalf("Send after redial: %v", err)
	}
	if dials != 2 {
		t.Errorf("dials = %d, want 2", dials)
	}
}

func TestSoundPlayback(t *testing.T) {
	t.Run("played on success", func(t *testing.T) {
		bus := newFakeBus()
		var played []string
		n := New(Options{
			Connect: func() (*Conn, error) { return bus.conn(), nil },
			Play:    func(path string) { played = append(played, path) },
		})
		defer n.Close()
		if err := n.Send(Request{Summary: "s", SoundPath: "/tmp/beep.wav"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if len(played) != 1 || played[0] != "/tmp/beep.wav" {
			t.Errorf("played = %v, want [/tmp/beep.wav]", played)
		}
	})
	t.Run("silent without a sound path", func(t *testing.T) {
		bus := newFakeBus()
		var played []string
		n := New(Options{
			Connect: func() (*Conn, error) { return bus.conn(), nil },
			Play:    func(path string) { played = append(played, path) },
		})
		defer n.Close()
		if err := n.Send(Request{Summary: "s"}); err != nil {
			t.Fatalf("Send: %v", err)
		}
		if len(played) != 0 {
			t.Errorf("played = %v, want none", played)
		}
	})
	t.Run("silent when the send fails", func(t *testing.T) {
		broken := &Conn{
			Notify: func(uint32, string, string, string, []string, map[string]dbus.Variant, int32) (uint32, error) {
				return 0, errors.New("daemon gone")
			},
			Signals: make(chan Signal),
			Close:   func() {},
		}
		var played []string
		n := New(Options{
			Connect: func() (*Conn, error) { return broken, nil },
			Play:    func(path string) { played = append(played, path) },
		})
		defer n.Close()
		if err := n.Send(Request{Summary: "s", SoundPath: "/tmp/beep.wav"}); err == nil {
			t.Fatal("Send succeeded on broken conn")
		}
		if len(played) != 0 {
			t.Errorf("played = %v, want none on a failed send", played)
		}
	})
}

func TestSendAfterClose(t *testing.T) {
	n := New(Options{Connect: func() (*Conn, error) {
		t.Fatal("Connect called after Close")
		return nil, nil
	}})
	n.Close()
	if err := n.Send(Request{Summary: "s"}); err == nil {
		t.Fatal("Send succeeded after Close")
	}
}

func TestEmptySummaryRejected(t *testing.T) {
	n := New(Options{Connect: func() (*Conn, error) {
		t.Fatal("Connect called for empty summary")
		return nil, nil
	}})
	defer n.Close()
	if err := n.Send(Request{}); err == nil {
		t.Fatal("Send accepted empty summary")
	}
}

func TestParseUrgency(t *testing.T) {
	tests := []struct {
		in   string
		want Urgency
	}{
		{"low", UrgencyLow},
		{"normal", UrgencyNormal},
		{"critical", UrgencyCritical},
		{"", UrgencyNormal},
		{"future-level", UrgencyNormal},
	}
	for _, tt := range tests {
		if got := ParseUrgency(tt.in); got != tt.want {
			t.Errorf("ParseUrgency(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
