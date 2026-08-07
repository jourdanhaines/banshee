// Package launch executes result actions: detached processes, terminal
// spawns, URLs, signals. Action kinds map to handlers through a Dispatcher
// so new kinds are one Register call (see internal/providers.Action).
//
// dispatch.go is a frozen Phase-0 contract; the built-in handlers land in
// Phase 1.
package launch

import (
	"fmt"

	"github.com/jourdanhaines/banshee/internal/providers"
)

// Handler executes one action kind.
type Handler func(a providers.Action) error

// Dispatcher routes actions to handlers by Kind.
type Dispatcher struct {
	handlers map[string]Handler
}

// NewDispatcher returns an empty Dispatcher; handlers are added via Register.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{handlers: map[string]Handler{}}
}

// Register binds kind to h, replacing any previous handler.
func (d *Dispatcher) Register(kind string, h Handler) {
	d.handlers[kind] = h
}

// Dispatch runs the handler for a.Kind.
func (d *Dispatcher) Dispatch(a providers.Action) error {
	h, ok := d.handlers[a.Kind]
	if !ok {
		return fmt.Errorf("launch: no handler for action kind %q", a.Kind)
	}
	return h(a)
}
