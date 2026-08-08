package secrets

import (
	"context"
	"errors"
	"fmt"

	gokeyring "github.com/zalando/go-keyring"
)

// keyringService is the Secret Service collection attribute every banshee
// secret is filed under, so `secret-tool search service banshee` finds them all
// and an uninstall can clean up. It is part of the on-disk identity of stored
// items and must not change.
const keyringService = "banshee"

// keyringClient is the three calls this backend needs from a Secret Service
// implementation. It exists so the tests can drive Keyring with a fake and stay
// hermetic — a real go-keyring call talks to DBus, which no test in this repo
// is allowed to require.
type keyringClient interface {
	Set(service, user, secret string) error
	Get(service, user string) (string, error)
	Delete(service, user string) error
}

// gokeyringClient adapts the package-level functions of zalando/go-keyring to
// keyringClient. The upstream API is free functions, not a value, so the
// adapter is the only way to name it behind an interface.
type gokeyringClient struct{}

func (gokeyringClient) Set(service, user, secret string) error {
	return gokeyring.Set(service, user, secret)
}

func (gokeyringClient) Get(service, user string) (string, error) {
	return gokeyring.Get(service, user)
}

func (gokeyringClient) Delete(service, user string) error {
	return gokeyring.Delete(service, user)
}

// Keyring stores secrets in the OS Secret Service (gnome-keyring, KeePassXC,
// KWallet's Secret Service shim). It is the default recommendation: the
// material is encrypted at rest and the unlock policy belongs to the desktop
// session rather than to banshee.
//
// The Secret Service offers no enumeration here — the item key is the whole
// lookup — which is fine because the non-secret TOTP index already owns the
// list of names.
type Keyring struct {
	client keyringClient
}

var _ Store = (*Keyring)(nil)

// NewKeyring returns a Keyring talking to the real Secret Service over DBus.
func NewKeyring() *Keyring { return &Keyring{client: gokeyringClient{}} }

// newKeyringWith returns a Keyring driven by client. Unexported because the
// substitution exists for this package's tests, not as a public seam: callers
// choosing a backend go through Open.
func newKeyringWith(client keyringClient) *Keyring { return &Keyring{client: client} }

// Name identifies the backend for persistence in the TOTP index.
func (k *Keyring) Name() string { return BackendKeyring }

// AuthPerAccess is false: unlocking is the session's business, and once the
// collection is unlocked reads succeed without banshee holding a password.
func (k *Keyring) AuthPerAccess() bool { return false }

// Blocking is true: a Secret Service call is a synchronous DBus round trip, and
// a locked collection turns it into an unbounded wait on a user-facing unlock
// prompt (see run). Callers on a UI thread must detach.
func (k *Keyring) Blocking() bool { return true }

// Get returns the secret filed under key, or ErrNotFound. cred is ignored.
func (k *Keyring) Get(ctx context.Context, key string, _ Credential) (string, error) {
	var v string
	err := k.run(ctx, func() error {
		var err error
		v, err = k.client.Get(keyringService, key)
		return err
	})
	if err != nil {
		return "", k.wrap("get", key, err)
	}
	return v, nil
}

// Set stores value under key, replacing any previous item.
func (k *Keyring) Set(ctx context.Context, key, value string, _ Credential) error {
	err := k.run(ctx, func() error { return k.client.Set(keyringService, key, value) })
	return k.wrap("set", key, err)
}

// Delete removes the item filed under key.
func (k *Keyring) Delete(ctx context.Context, key string, _ Credential) error {
	err := k.run(ctx, func() error { return k.client.Delete(keyringService, key) })
	return k.wrap("delete", key, err)
}

// run executes fn on its own goroutine and returns as soon as either fn
// finishes or ctx is done. The indirection exists because a Secret Service call
// is a synchronous DBus round trip with no context support: a locked collection
// can sit on a user prompt indefinitely, and a provider Query that blocks there
// would stall the aggregator's errgroup Wait — meaning one hung keyring freezes
// every other provider's results.
//
// Tradeoff: on ctx cancellation the goroutine is left running until the DBus
// call returns, so its result is discarded rather than aborted. That leaks one
// goroutine (and the buffered channel it writes to) per abandoned call. It is
// the lesser cost — the alternative is a frozen launcher — and the leak is
// bounded in practice because a prompt eventually resolves or times out.
func (k *Keyring) run(ctx context.Context, fn func() error) error {
	done := make(chan error, 1) // buffered: the goroutine must never block on an abandoned receive
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wrap maps backend errors onto this package's sentinels. A missing item is
// ErrNotFound; anything else keeps its cause but gains the hint that most
// failures here are a missing or locked Secret Service daemon rather than a bad
// key. The secret value itself is never part of the message.
func (k *Keyring) wrap(op, key string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gokeyring.ErrNotFound):
		return fmt.Errorf("keyring: %q: %w", key, ErrNotFound)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return fmt.Errorf("keyring %s %q: %w (is a Secret Service (gnome-keyring/KeePassXC) running?)", op, key, err)
	}
}
