package secrets

import (
	"context"
	"fmt"
)

// NimbusConfig holds the settings a working Nimbus backend will need. The
// fields are reserved now, while nothing reads them, so the setup flow and any
// persisted backend record can be extended without a second migration once the
// implementation lands.
type NimbusConfig struct {
	// URL is the Nimbus endpoint the secret is fetched from.
	URL string
	// Token is the long-lived client identity presented to that endpoint. It
	// authenticates the machine, not the access — see KeyPath.
	Token string
	// KeyPath is the local key file that decrypts the fetched material and
	// whose use is guarded by the per-access sudo password.
	KeyPath string
}

// Nimbus is the remote, per-access-authenticated backend. It is a stub: every
// operation reports ErrNotConfigured. It ships ahead of its implementation on
// purpose, because AuthPerAccess() == true is the only thing in the tree that
// exercises the credential-form path — the provider's masked-password rows are
// written, tested and reachable today rather than bolted on later.
//
// # Future contract
//
// When implemented, an access will run in this order, and callers already
// written against this type must not assume otherwise:
//
//   - Every operation needs a fresh Credential.Password. The local key is
//     sudo-guarded, and Nimbus deliberately does no caching and no session
//     unlock: there is no Unlock method and none will be added, because the
//     protection the design buys is exactly "a password per access".
//   - The password reaches sudo on stdin only (`sudo -S`, or a helper reading
//     stdin). It must never appear in an argv, an environment variable, a
//     temporary file, or a log line.
//   - A missing or empty Credential is ErrAuthRequired, and so is a rejected
//     password — the caller re-prompts rather than reporting a lookup failure.
//   - Get then performs a network fetch and can take seconds. Callers must pass
//     a real ctx with a timeout, and must not call it on the GTK main loop.
type Nimbus struct {
	cfg NimbusConfig
}

var _ Store = (*Nimbus)(nil)

// NewNimbus returns the Nimbus stub. cfg is retained unused so that wiring
// written against it today keeps compiling when the backend gains a body.
func NewNimbus(cfg NimbusConfig) *Nimbus { return &Nimbus{cfg: cfg} }

// Name identifies the backend for persistence in the TOTP index.
func (n *Nimbus) Name() string { return BackendNimbus }

// AuthPerAccess is true: the future backend re-authenticates on every single
// operation, so the UI must collect a password before each one.
func (n *Nimbus) AuthPerAccess() bool { return true }

// Blocking is true: an access is a network fetch guarded by a sudo prompt, so
// it can take seconds. It is already true for the stub so that callers written
// against it today take the detached path (see the future contract above).
func (n *Nimbus) Blocking() bool { return true }

// Get is not implemented and reports ErrNotConfigured.
func (n *Nimbus) Get(context.Context, string, Credential) (string, error) {
	return "", n.unavailable()
}

// Set is not implemented and reports ErrNotConfigured.
func (n *Nimbus) Set(context.Context, string, string, Credential) error {
	return n.unavailable()
}

// Delete is not implemented and reports ErrNotConfigured.
func (n *Nimbus) Delete(context.Context, string, Credential) error {
	return n.unavailable()
}

// unavailable is the single message the stub reports, phrased for a user who
// picked Nimbus in the setup list rather than for a developer.
func (n *Nimbus) unavailable() error {
	return fmt.Errorf("Nimbus is not available yet: %w", ErrNotConfigured)
}
