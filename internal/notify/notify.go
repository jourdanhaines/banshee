// Package notify sends desktop notifications over the session bus's
// org.freedesktop.Notifications service (mako, dunst, swaync, …).
//
// It exists because neither of the alternatives can express the notification
// plugin contract: gio.Notification has no expire-timeout or persistence
// surface at all, and notify-send cannot report an invoked action without
// blocking until the notification closes. Talking to the daemon directly
// gives us expire_timeout, urgency, resident notifications and the
// ActionInvoked/NotificationClosed signals.
//
// The package is GTK-free on purpose: Send is called from plugin readLoop
// goroutines and must never need the GTK main loop.
package notify

import (
	"errors"
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	dbusService   = "org.freedesktop.Notifications"
	dbusPath      = "/org/freedesktop/Notifications"
	dbusInterface = "org.freedesktop.Notifications"
	appName       = "banshee"
)

// Urgency is a notification urgency level. The zero value is Normal so a
// Request that never mentions urgency gets the daemon's ordinary treatment.
type Urgency uint8

const (
	// UrgencyNormal is the default level.
	UrgencyNormal Urgency = iota
	// UrgencyLow marks informational notifications.
	UrgencyLow
	// UrgencyCritical marks notifications daemons keep on screen until the
	// user dismisses them.
	UrgencyCritical
)

// ParseUrgency maps the wire strings "low", "normal" and "critical" to an
// Urgency. Anything else — including empty — is Normal, so an unknown future
// level degrades rather than errors.
func ParseUrgency(s string) Urgency {
	switch s {
	case "low":
		return UrgencyLow
	case "critical":
		return UrgencyCritical
	default:
		return UrgencyNormal
	}
}

// hintByte is the spec's byte encoding for the "urgency" hint.
func (u Urgency) hintByte() byte {
	switch u {
	case UrgencyLow:
		return 0
	case UrgencyCritical:
		return 2
	default:
		return 1
	}
}

// Action is one button on a notification. The key "default" is special-cased
// by daemons as the body-click action.
type Action struct {
	Key   string
	Label string
}

// Event reports something the user did to a sent notification: either an
// action was invoked (ActionKey set) or the notification closed (Closed set,
// with the spec's reason code: 1 expired, 2 dismissed, 3 closed by call,
// 4 undefined).
type Event struct {
	ActionKey string
	Closed    bool
	Reason    int
}

// Request describes one notification to send.
type Request struct {
	// Key, when set, makes a later Send with the same Key replace this
	// notification on screen instead of stacking a new one.
	Key     string
	Summary string
	Body    string
	// Icon is an icon-theme name or an absolute file path.
	Icon string
	// Urgency defaults to Normal.
	Urgency Urgency
	// RequireInput keeps the notification on screen until the user acts on
	// it: expire_timeout 0 (never expire), the resident hint, and Critical
	// urgency unless Urgency was set to something else.
	RequireInput bool
	// TimeoutMS expires the notification after that many milliseconds.
	// Ignored when RequireInput is set; zero means the daemon's default.
	TimeoutMS int
	Actions []Action
	// OnEvent, when set, is invoked for this notification's action and close
	// events. It runs on the notifier's signal goroutine — never the GTK main
	// loop — and is dropped once the notification closes.
	OnEvent func(Event)
}

// Signal is one raw daemon signal delivered by a Conn.
type Signal struct {
	ID        uint32
	ActionKey string
	Closed    bool
	Reason    int
}

// Conn is a live connection to a notification daemon. It is a struct of
// function values (the cliphist WatcherOptions precedent) so tests can fake
// the bus without an interface.
type Conn struct {
	// Notify performs one org.freedesktop.Notifications.Notify call and
	// returns the daemon-assigned notification id.
	Notify func(replacesID uint32, summary, body, icon string,
		actions []string, hints map[string]dbus.Variant, expire int32) (uint32, error)
	// Signals delivers ActionInvoked/NotificationClosed signals until the
	// connection closes, after which the channel is closed.
	Signals <-chan Signal
	// Close tears the connection down.
	Close func()
}

// Options tunes a Notifier. The zero value dials the real session bus.
type Options struct {
	// Connect dials the notification daemon; nil dials the session bus.
	Connect func() (*Conn, error)
	// Log receives diagnostics; nil discards them.
	Log func(format string, args ...any)
}

// Notifier sends notifications and routes their action/close signals back to
// per-notification callbacks. New performs no I/O: the bus is dialed lazily
// on the first Send and redialed on the next Send after a failure, so
// constructing one in tests (or on a bus-less system) costs nothing.
type Notifier struct {
	opts Options

	mu       sync.Mutex
	conn     *Conn
	gen      uint64 // connection generation; stale dispatch goroutines exit
	handlers map[uint32]func(Event)
	keys     map[string]uint32
	closed   bool
}

// New returns a Notifier. No connection is made until the first Send.
func New(opts Options) *Notifier {
	if opts.Connect == nil {
		opts.Connect = sessionConnect
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	return &Notifier{
		opts:     opts,
		handlers: map[uint32]func(Event){},
		keys:     map[string]uint32{},
	}
}

// Send delivers one notification, dialing the bus first if needed. On failure
// the connection is dropped so the next Send redials once; the caller decides
// whether the error is worth more than a log line.
func (n *Notifier) Send(req Request) error {
	if req.Summary == "" {
		return errors.New("notify: empty summary")
	}

	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return errors.New("notify: notifier closed")
	}
	if n.conn == nil {
		conn, err := n.opts.Connect()
		if err != nil {
			n.mu.Unlock()
			return fmt.Errorf("notify: %w", err)
		}
		n.conn = conn
		n.gen++
		go n.dispatch(n.gen, conn.Signals)
	}
	conn, gen := n.conn, n.gen
	replaces := n.keys[req.Key] // zero for "" or an unseen key: a fresh notification
	n.mu.Unlock()

	actions := make([]string, 0, len(req.Actions)*2)
	for _, a := range req.Actions {
		actions = append(actions, a.Key, a.Label)
	}
	urgency := req.Urgency
	expire := int32(-1)
	hints := map[string]dbus.Variant{}
	switch {
	case req.RequireInput:
		expire = 0
		hints["resident"] = dbus.MakeVariant(true)
		if urgency == UrgencyNormal {
			urgency = UrgencyCritical
		}
	case req.TimeoutMS > 0:
		expire = int32(req.TimeoutMS)
	}
	hints["urgency"] = dbus.MakeVariant(urgency.hintByte())

	// The bus call runs without n.mu held: the dispatch goroutine needs the
	// lock to route signals, and a slow daemon must not stall it.
	id, err := conn.Notify(replaces, req.Summary, req.Body, req.Icon, actions, hints, expire)

	n.mu.Lock()
	if err != nil {
		if n.conn == conn {
			n.dropConnLocked()
		}
		n.mu.Unlock()
		return fmt.Errorf("notify: %w", err)
	}
	if n.gen == gen { // otherwise redialed or closed underneath us; the send still landed
		if req.Key != "" {
			n.keys[req.Key] = id
		}
		if req.OnEvent != nil {
			n.handlers[id] = req.OnEvent
		} else {
			delete(n.handlers, id)
		}
	}
	n.mu.Unlock()
	return nil
}

// Close drops the connection and every registered callback. The Notifier is
// unusable afterwards.
func (n *Notifier) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = true
	n.dropConnLocked()
	n.handlers = map[uint32]func(Event){}
	n.keys = map[string]uint32{}
}

// dropConnLocked closes the current connection and bumps the generation so
// its dispatch goroutine exits. Notification ids are assigned by the daemon,
// not the connection, so the key map survives — replacement keeps working
// after a redial.
func (n *Notifier) dropConnLocked() {
	if n.conn == nil {
		return
	}
	n.conn.Close()
	n.conn = nil
	n.gen++
}

// dispatch routes one connection's signals to the registered callbacks. It
// exits when the connection's signal channel closes or the generation moves
// on (redial, Close).
func (n *Notifier) dispatch(gen uint64, sigs <-chan Signal) {
	for s := range sigs {
		n.mu.Lock()
		if n.gen != gen {
			n.mu.Unlock()
			return
		}
		h := n.handlers[s.ID]
		if s.Closed {
			delete(n.handlers, s.ID)
			for k, id := range n.keys {
				if id == s.ID {
					delete(n.keys, k)
				}
			}
		}
		n.mu.Unlock()
		if h == nil {
			continue
		}
		if s.Closed {
			h(Event{Closed: true, Reason: s.Reason})
		} else {
			h(Event{ActionKey: s.ActionKey})
		}
	}
}

// Probe dials the session bus and asks the notification daemon to identify
// itself. It is `banshee doctor`'s health check.
func Probe() (server string, err error) {
	bus, err := dialSessionBus()
	if err != nil {
		return "", err
	}
	defer bus.Close()
	var name, vendor, version, spec string
	obj := bus.Object(dbusService, dbus.ObjectPath(dbusPath))
	if err := obj.Call(dbusInterface+".GetServerInformation", 0).Store(&name, &vendor, &version, &spec); err != nil {
		return "", err
	}
	return name + " " + version, nil
}

// dialSessionBus opens a private session-bus connection. Private rather than
// the process-shared dbus.SessionBus so Close never tears the bus out from
// under another package (the keyring backend shares the same module).
func dialSessionBus() (*dbus.Conn, error) {
	bus, err := dbus.SessionBusPrivate()
	if err != nil {
		return nil, err
	}
	if err := bus.Auth(nil); err != nil {
		bus.Close()
		return nil, err
	}
	if err := bus.Hello(); err != nil {
		bus.Close()
		return nil, err
	}
	return bus, nil
}

// sessionConnect is the real Options.Connect: a private session-bus
// connection subscribed to the daemon's action/close signals.
func sessionConnect() (*Conn, error) {
	bus, err := dialSessionBus()
	if err != nil {
		return nil, err
	}
	if err := bus.AddMatchSignal(
		dbus.WithMatchInterface(dbusInterface),
		dbus.WithMatchObjectPath(dbus.ObjectPath(dbusPath)),
	); err != nil {
		bus.Close()
		return nil, err
	}

	raw := make(chan *dbus.Signal, 32)
	bus.Signal(raw)
	out := make(chan Signal, 32)
	go func() {
		defer close(out)
		for s := range raw {
			switch s.Name {
			case dbusInterface + ".ActionInvoked":
				if len(s.Body) != 2 {
					continue
				}
				id, ok1 := s.Body[0].(uint32)
				key, ok2 := s.Body[1].(string)
				if ok1 && ok2 {
					out <- Signal{ID: id, ActionKey: key}
				}
			case dbusInterface + ".NotificationClosed":
				if len(s.Body) != 2 {
					continue
				}
				id, ok1 := s.Body[0].(uint32)
				reason, ok2 := s.Body[1].(uint32)
				if ok1 && ok2 {
					out <- Signal{ID: id, Closed: true, Reason: int(reason)}
				}
			}
		}
	}()

	obj := bus.Object(dbusService, dbus.ObjectPath(dbusPath))
	call := func(replacesID uint32, summary, body, icon string,
		actions []string, hints map[string]dbus.Variant, expire int32) (uint32, error) {
		var id uint32
		err := obj.Call(dbusInterface+".Notify", 0,
			appName, replacesID, icon, summary, body, actions, hints, expire).Store(&id)
		return id, err
	}
	return &Conn{
		Notify:  call,
		Signals: out,
		Close:   func() { _ = bus.Close() },
	}, nil
}
