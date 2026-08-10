// Package secrets stores the small pieces of secret material banshee holds on
// a user's behalf — today the shared keys behind the TOTP provider — behind one
// interface with several interchangeable backends. It exists because the right
// place for a TOTP key is a user decision, not a banshee decision: a throwaway
// key on a single-user laptop belongs in a plain file, a real one belongs in
// the session keyring, and a shared one belongs in a remote vault that
// re-authenticates on every read. Store is the seam those three answers share.
//
// # Key convention
//
// Keys are namespaced by owner with a slash: the TOTP provider stores entry
// "github" under "totp/github". Backends treat the key as an opaque string, so
// a future owner only has to pick a prefix nobody else uses. The non-secret
// index that maps human names to keys lives outside this package (see
// internal/providers/totp) — a Store holds secret material only, and cannot be
// enumerated.
//
// # Threat model
//
// What a Store defends against depends on the backend, and callers must not
// assume more than the one they opened provides:
//
//   - Plaintext defends against nothing but accidental disclosure. The key sits
//     in a 0600 file under a 0700 directory, readable by anything running as the
//     user (and by root). It is the honest equivalent of a dotfile: convenient,
//     and no stronger than the account it lives in.
//   - Keyring defends against at-rest disclosure while the collection is locked,
//     and delegates unlock policy to the Secret Service implementation. Once the
//     session collection is unlocked, any process in the session can read it.
//   - Nimbus (not yet implemented) defends against a stolen laptop: the material
//     is remote and each access is separately authenticated, so a copied disk
//     yields nothing.
//
// No backend defends against a compromised banshee process, and none of them
// erase secrets from memory — Go strings are immutable and may be copied by the
// garbage collector, so a decoded key lives until collection. This matches the
// guarantee the clipboard already gives (see launch.CopyToClipboard) and is a
// deliberate accepted limit, not an oversight.
//
// # Credential hygiene
//
// Secret values and Credential passwords follow the same rule as
// launch.CopyToClipboard: they travel on stdin, in memory, or over an
// authenticated socket — never in a process argv, never in a log line, never in
// an error message. Errors returned from this package name the key and the
// backend, never the value. Anything logging an Action's Values map, or
// formatting a Credential with %v, breaks the rule.
package secrets

import (
	"context"
	"errors"
	"fmt"
)

// Backend names accepted by Open. They are the values persisted in the TOTP
// index, so they are part of an on-disk format and must not be renamed.
const (
	// BackendPlaintext names the local unencrypted JSON file backend.
	BackendPlaintext = "plaintext"
	// BackendKeyring names the OS Secret Service backend.
	BackendKeyring = "keyring"
	// BackendNimbus names the remote per-access-authenticated backend.
	BackendNimbus = "nimbus"
)

// Credential carries the per-access authentication a backend demands. It is a
// struct rather than a bare string so a backend that later needs a second
// factor can grow a field without breaking every caller, and so the type name
// marks the value as sensitive at every call site it passes through.
//
// The zero Credential means "none supplied". Local backends ignore it entirely;
// backends reporting AuthPerAccess() == true reject the zero value with
// ErrAuthRequired.
type Credential struct {
	// Password is the secret the backend needs to unlock this one access —
	// for Nimbus, the sudo password guarding the local key. It must never be
	// placed in an argv, a log line, or an error string.
	Password string
}

// Errors a Store distinguishes. Callers branch on these with errors.Is;
// backends wrap them with context so the message can name the backend and key
// while the sentinel survives.
var (
	// ErrAuthRequired reports that the backend needs a Credential (or was
	// given a wrong one). The provider turns this into a masked credential
	// form on the row rather than an error notification.
	ErrAuthRequired = errors.New("secrets: authentication required")
	// ErrNotConfigured reports that the backend cannot be used as it stands —
	// no daemon running, no endpoint configured, or not implemented yet. It
	// is a setup problem, not a lookup failure.
	ErrNotConfigured = errors.New("secrets: backend not configured")
	// ErrNotFound reports that the backend works but holds no value for the
	// key. Distinct from ErrNotConfigured so the caller can tell "your vault
	// is broken" from "you never added that entry".
	ErrNotFound = errors.New("secrets: key not found")
)

// Store is the secret-material seam: a keyed get/set/delete over some backing
// vault. It is one of the few genuine multi-implementation interfaces in
// banshee — plaintext, keyring and nimbus are real alternatives a user chooses
// between, not a mock and a real thing.
//
// Implementations must honor ctx: Get in particular may block on IPC to a
// keyring daemon or on a network round trip, and it is called from provider
// query goroutines that the aggregator cancels on every keystroke.
type Store interface {
	// Name returns the backend identifier — one of the Backend* constants.
	// It is what gets persisted so the same backend is reopened next run.
	Name() string
	// Get returns the value stored under key, or ErrNotFound. cred is
	// ignored by backends that do not authenticate per access.
	Get(ctx context.Context, key string, cred Credential) (string, error)
	// Set stores value under key, replacing any previous value. It is
	// idempotent so a caller whose follow-up bookkeeping failed can retry.
	Set(ctx context.Context, key, value string, cred Credential) error
	// Delete removes key. Deleting an absent key returns ErrNotFound.
	Delete(ctx context.Context, key string, cred Credential) error
	// AuthPerAccess reports whether every single operation needs a fresh
	// Credential. True forces the UI to collect a password before each read,
	// which is why it is part of the interface rather than a backend detail:
	// the provider shapes its rows around the answer.
	AuthPerAccess() bool
	// Blocking reports whether an operation can wait on something outside this
	// process — an interactive keyring unlock prompt, a network round trip —
	// rather than completing as a bounded local read or write. It is part of
	// the interface for the same reason AuthPerAccess is: callers shape their
	// control flow around it. A true answer means the call must not be made
	// from a UI thread; banshee's action handlers run on the GTK main loop and
	// detach every Blocking backend onto a goroutine, because a blocked main
	// loop freezes the whole daemon, IPC included.
	Blocking() bool
}

// Open constructs the backend named by name. It is the single place backend
// names are resolved, so the persisted TOTP index and the setup flow agree on
// the spelling without either importing the other's constants.
//
// An unknown name is an error rather than a silent fallback: silently
// downgrading to plaintext because a config typo hid the keyring would write a
// secret somewhere the user did not ask for.
func Open(name string) (Store, error) {
	switch name {
	case BackendPlaintext:
		return NewPlaintext(""), nil
	case BackendKeyring:
		return NewKeyring(), nil
	case BackendNimbus:
		return NewNimbus(NimbusConfig{}), nil
	default:
		return nil, fmt.Errorf("unknown secrets backend %q (want %q, %q or %q)",
			name, BackendPlaintext, BackendKeyring, BackendNimbus)
	}
}
