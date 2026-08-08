package totp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// Action kinds emitted by this provider.
const (
	// ActTOTPCopy computes the current code for Argv[0] and copies it.
	// Values["credential"] carries the unlock password, when the backend
	// demands one.
	ActTOTPCopy = "totp-copy"
	// ActTOTPAdd stores a new entry. Values carry the submitted form:
	// "name", "secret" and optionally "credential".
	ActTOTPAdd = "totp-add"
	// ActTOTPSetup records Argv[0] as the secrets backend to use.
	ActTOTPSetup = "totp-setup"
)

// Timeouts bounding the backend calls the handlers make. They are generous
// because a keyring may prompt the user to unlock a collection, and short
// enough that a wedged Secret Service daemon cannot leak goroutines forever.
const (
	copyTimeout  = 15 * time.Second
	addTimeout   = 15 * time.Second
	probeTimeout = 15 * time.Second
	// fixTimeout bounds the repair command a wizard fix row runs, and nothing
	// else. The setup that follows a successful fix takes a fresh probeTimeout
	// rather than the remainder of this one: a repair that succeeded after
	// fourteen seconds would otherwise hand the re-probe a second, report the
	// keyring it had just fixed as broken, and abandon the probe's write with no
	// matching delete behind it. The fix's auto-retry is a retry, so it gets
	// exactly the budget pressing Retry gets.
	fixTimeout = 15 * time.Second
)

// fixOutputLimit caps how much of a failed fix command's output is folded into
// the message the wizard shows. The output lands in a single-line result
// subtitle, so this is a diagnosis — the first thing systemctl or the daemon
// complained about — and not a pager.
const fixOutputLimit = 200

// probeKey is written and immediately deleted to prove a backend works before
// it is persisted as the user's choice. It lives in this package's key
// namespace so a leftover probe — a crash between Set and Delete, or a backend
// that accepts writes but refuses deletes — is recognizably ours, and its
// double-underscore spelling keeps it out of the way of a plausible entry name.
const probeKey = "totp/__probe__"

// reopenQuery is what the launcher is reopened with after a backend failure or
// a completed setup: the provider's own trigger word, so the reopened window
// lands on this provider's rows (the wizard, or the ordinary entry/add rows once
// the backend works) rather than an empty list.
const reopenQuery = "totp"

// Deps are the handlers' collaborators. Every one is injectable because the
// handlers are the only part of this package that touches the clock, the
// clipboard, the user's disk, a real vault and a real subprocess — the tests
// replace all five.
type Deps struct {
	// IndexPath is the totp.json to read and write.
	IndexPath string
	// OpenStore resolves a backend name to a Store. Defaults to secrets.Open.
	OpenStore func(name string) (secrets.Store, error)
	// Copy puts text on the system clipboard. Defaults to the launch
	// clipboard helper, which keeps the code off every process's argv.
	Copy func(text string) error
	// Now supplies the instant a code is computed for. Defaults to time.Now.
	Now func() time.Time
	// Run executes a wizard-fix command to completion and returns whatever it
	// printed. Defaults to exec.CommandContext plus CombinedOutput.
	//
	// argv never comes from an Action: the handler resolves it out of
	// wizardFixes, so the only commands reachable here are the ones this
	// package wrote down. The output is never shown on its own and never
	// logged — it is only ever folded, truncated, into the error message the
	// wizard re-renders with, because a fix command's stderr is the one place
	// that says *why* it refused. Tests inject a recorder instead, which is what
	// keeps this suite from starting daemons on the developer's machine.
	Run func(ctx context.Context, argv []string) ([]byte, error)
	// RunInput is Run for the fixes that read a secret from standard input
	// (wizardFix.stdin): the submitted keyring password is written to the
	// command's stdin and nowhere else — never argv, never the environment,
	// never a log line — the same rule the clipboard and the secret stores
	// follow. The command's output is folded into error messages exactly like
	// Run's; the fixes that use this (gnome-keyring-daemon --unlock) do not
	// echo their stdin, and nothing user-typed may be added to a fix whose
	// output can. Defaults to exec.CommandContext with a string reader.
	RunInput func(ctx context.Context, argv []string, input string) ([]byte, error)
	// Notify reports an asynchronous failure to the user. The launcher window
	// is already hidden by the time a detached copy fails, so a returned error
	// would go nowhere; this is the only channel back. Defaults to the log.
	Notify func(msg string)
	// State is the failure record the provider reads to decide whether to
	// render the setup wizard. Nil switches the wizard off — every method is
	// nil-receiver-safe, so an unwired front-end keeps the toast behavior.
	State *SetupState
	// Reopen shows the launcher again on the given query, which is how a
	// recorded failure becomes visible rows instead of a toast. Nil switches
	// the wizard off in the same way State does.
	//
	// It is called from detached handler goroutines, so an implementation that
	// touches a UI toolkit owns the hop back to its main loop (boot wraps
	// glib.IdleAdd); this package never assumes which thread it runs on.
	Reopen func(query string)
}

// withDefaults fills the nil collaborators, so RegisterHandlersWith callers
// (tests) can supply only what they assert on.
//
// State and Reopen are deliberately not defaulted: there is no sensible stand-in
// for "show the launcher again", and a caller that did not wire them wants the
// wizard off rather than a half-wired version of it.
func (d Deps) withDefaults() Deps {
	if d.IndexPath == "" {
		d.IndexPath = config.TOTPIndexPath()
	}
	if d.OpenStore == nil {
		d.OpenStore = secrets.Open
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Notify == nil {
		d.Notify = func(msg string) { log.Printf("totp: %s", msg) }
	}
	if d.Copy == nil {
		d.Copy = func(string) error { return errors.New("totp: no clipboard configured") }
	}
	if d.Run == nil {
		d.Run = func(ctx context.Context, argv []string) ([]byte, error) {
			if len(argv) == 0 {
				return nil, errors.New("totp: no command to run")
			}
			// No shell: argv comes from wizardFixes already split, and running
			// it through one would only add a layer that could word-split or
			// expand something.
			return exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
		}
	}
	if d.RunInput == nil {
		d.RunInput = func(ctx context.Context, argv []string, input string) ([]byte, error) {
			if len(argv) == 0 {
				return nil, errors.New("totp: no command to run")
			}
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Stdin = strings.NewReader(input)
			return cmd.CombinedOutput()
		}
	}
	return d
}

// trimmedOutput renders a fix command's output as the tail of an error message:
// empty for a command that said nothing, and otherwise ": " plus at most
// fixOutputLimit bytes of it.
//
// Every run of whitespace is collapsed to a single space first, interior
// newlines included, because the destination is a single-line ellipsized result
// subtitle: a second line would not be rendered at all, so leaving it in would
// spend the byte budget on text the user cannot read and push the part that
// matters past the limit. Collapsing also folds systemd's usual two-line
// "it failed / see journalctl" pair into one readable sentence.
//
// The truncation is by bytes and then re-validated as UTF-8, because a cut in
// the middle of a multi-byte rune would put a replacement character into a
// string that ends up in daemon-lifetime state and on screen.
func trimmedOutput(out []byte) string {
	s := strings.Join(strings.Fields(string(out)), " ")
	if s == "" {
		return ""
	}
	if len(s) > fixOutputLimit {
		s = strings.ToValidUTF8(s[:fixOutputLimit], "") + "…"
	}
	return ": " + s
}

// RegisterHandlers binds every TOTP action kind on d, wired to the real
// clipboard, index and secrets backends. It leaves the wizard collaborators
// unset, so a caller that wants the setup wizard uses RegisterHandlersWith.
func RegisterHandlers(d *launch.Dispatcher, opts launch.Options) {
	RegisterHandlersWith(d, Deps{
		Copy: func(text string) error { return launch.CopyToClipboard(opts, text) },
	})
}

// RegisterHandlersWith is RegisterHandlers with injectable collaborators, for
// tests and for a front-end that routes notifications somewhere other than the
// log (boot passes a closure onto the GTK main loop).
func RegisterHandlersWith(dispatch *launch.Dispatcher, deps Deps) {
	deps = deps.withDefaults()
	dispatch.Register(ActTOTPCopy, deps.handleCopy)
	dispatch.Register(ActTOTPAdd, deps.handleAdd)
	dispatch.Register(ActTOTPSetup, deps.handleSetup)
	dispatch.Register(ActTOTPWizardReset, deps.handleWizardReset)
	dispatch.Register(ActTOTPWizardFix, deps.handleWizardFix)
}

// backendUnusable reports whether err means the secrets backend itself is
// broken, as opposed to the request being wrong or the user changing their mind.
// It is the single rule that decides toast versus wizard.
//
// Not unusable: ErrNotFound (the backend answered, the key is simply absent),
// ErrAuthRequired (the backend is working and wants a password — the form
// already handles that), and context.Canceled (banshee gave up, the backend
// never said anything was wrong).
//
// DeadlineExceeded *is* unusable: a Secret Service that never answers inside the
// handler timeout is exactly the wedged-daemon case the wizard exists for, and
// telling the user "timed out" once in a toast leaves them nowhere to go.
//
// It is applied only at store.Get and store.Set call sites. An index read, a
// base32 decode or a clipboard write can fail for reasons that have nothing to
// do with the backend, and routing those to the wizard would blame the vault for
// a full disk.
func backendUnusable(err error) bool {
	return err != nil &&
		!errors.Is(err, secrets.ErrNotFound) &&
		!errors.Is(err, secrets.ErrAuthRequired) &&
		!errors.Is(err, context.Canceled)
}

// wizardWired reports whether both wizard collaborators are present. Either one
// missing means the wizard cannot happen — a recorded failure nobody renders, or
// a rendered failure nobody records — so the handlers fall back to the toast,
// which is what Deps.State and Deps.Reopen promise for an unwired front-end.
func (d Deps) wizardWired() bool { return d.State != nil && d.Reopen != nil }

// reportUnusable records a backend failure and puts it in front of the user as
// wizard rows.
//
// It falls back to the toast whenever the wizard would not actually appear: an
// unwired front-end (a headless caller, or a test that only asserts the old
// behavior), or a failure the provider's gate would discard — the index names a
// different backend now, because the user chose one while this call was still
// waiting on a wedged daemon. Reopening for a wizard that will not render would
// steal focus and report the failure nowhere at all, so those cases keep the
// toast, which is what makes a failure visible in every build.
func (d Deps) reportUnusable(backend string, err error) {
	if d.wizardWired() && d.wizardWouldRender(backend) {
		d.State.Fail(backend, err)
		d.Reopen(reopenQuery)
		return
	}
	d.Notify(err.Error())
}

// wizardWouldRender answers the provider's gate ahead of time, against the index
// as it stands now rather than the copy the caller loaded before the backend
// call it is reporting on. An unreadable index counts as no: the provider's own
// Query would fail on it too, so nothing would be rendered to see.
func (d Deps) wizardWouldRender(backend string) bool {
	idx, err := LoadIndex(d.IndexPath)
	if err != nil {
		return false
	}
	return wizardApplies(idx.Backend, backend)
}

// handleWizardReset abandons the wizard and returns the user to the ordinary
// rows — during first-time setup that means the backend chooser, because an
// index with no backend renders the setup rows once the failure is forgotten.
func (d Deps) handleWizardReset(providers.Action) error {
	d.State.Clear()
	if d.Reopen != nil {
		d.Reopen(reopenQuery)
	}
	return nil
}

// handleCopy copies the current code for the named entry.
//
// Everything that can fail cheaply and locally — a missing name, an unknown
// entry, an unopenable backend — is checked synchronously so the user sees a
// real error. The backend read is then detached: handlers run on the GTK main
// loop right after the window hides, and a keyring that decides to prompt
// would otherwise freeze the whole daemon. A detached failure reaches the user
// through Notify instead of a return value.
//
// The code is derived here, at dispatch time, rather than reused from the row
// the user looked at: a row rendered 29 seconds ago shows a code that is about
// to expire, and pasting a dead code is the one failure mode that makes a TOTP
// launcher useless.
func (d Deps) handleCopy(a providers.Action) error {
	if len(a.Argv) == 0 || strings.TrimSpace(a.Argv[0]) == "" {
		return errors.New("totp-copy: no entry name")
	}
	name := strings.TrimSpace(a.Argv[0])
	idx, err := LoadIndex(d.IndexPath)
	if err != nil {
		return err
	}
	if idx.Backend == "" {
		return errors.New("totp: no secrets backend configured yet")
	}
	entry, ok := idx.Find(name)
	if !ok {
		return fmt.Errorf("totp: no entry named %q", name)
	}
	store, err := d.OpenStore(idx.Backend)
	if err != nil {
		return err
	}
	cred := secrets.Credential{Password: a.Values["credential"]}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), copyTimeout)
		defer cancel()

		secret, err := store.Get(ctx, secretKey(entry.Name), cred)
		if err != nil {
			switch {
			case errors.Is(err, secrets.ErrAuthRequired):
				// First, and not through backendUnusable: a locked collection
				// is a working backend, and the user needs the password prompt,
				// not a page of "your keyring is broken" advice.
				d.Notify(fmt.Sprintf("%s needs your password to unlock %q", idx.Backend, entry.Name))
			case backendUnusable(err):
				// The entry names itself so the wizard header's subtitle says
				// which read failed; the secret never reaches this string
				// because it was never read.
				d.reportUnusable(idx.Backend, fmt.Errorf("could not read the secret for %q: %w", entry.Name, err))
			default:
				d.Notify(fmt.Sprintf("could not read the secret for %q: %v", entry.Name, err))
			}
			return
		}
		// A completed read is proof the backend works, so any recorded failure
		// is stale. Clearing here rather than after the copy is deliberate: a
		// bad base32 seed or a missing clipboard is not the backend's fault and
		// must not keep the wizard on screen.
		d.State.Clear()
		raw, err := DecodeSecret(secret)
		if err != nil {
			d.Notify(fmt.Sprintf("the stored secret for %q is not valid base32", entry.Name))
			return
		}
		code, err := Code(raw, d.Now(), entry.Params())
		if err != nil {
			d.Notify(fmt.Sprintf("could not compute a code for %q: %v", entry.Name, err))
			return
		}
		if err := d.Copy(code); err != nil {
			d.Notify(fmt.Sprintf("could not copy the code for %q: %v", entry.Name, err))
		}
	}()
	return nil
}

// handleAdd stores a new entry: seed into the secrets Store, metadata into the
// index.
//
// It runs synchronously for a non-Blocking backend — the same reasoning as
// connectors.RegisterLinkHandler, a couple of small local writes are safe on
// the GTK main loop, and staying synchronous guarantees the next query sees
// the new entry. A Blocking backend is detached instead: a Secret Service Set
// against a locked collection sits on a user-facing unlock prompt, and the
// timeout would hold the GTK main loop for its whole duration, freezing the
// daemon's IPC ops (see handleCopy). The condition is Blocking rather than
// AuthPerAccess because the keyring blocks without authenticating per access.
//
// Write order matters: the seed goes in first, then the index. A failed Set
// leaves no index entry pointing at a key that does not exist, and a failed
// index save after a successful Set is harmless to retry because Set is
// idempotent — the user simply submits the form again.
func (d Deps) handleAdd(a providers.Action) error {
	rawSecret := strings.TrimSpace(a.Values["secret"])
	if rawSecret == "" {
		return errors.New("totp: a secret is required")
	}
	seed, params, issuer, account, err := ParseSecretInput(rawSecret)
	if err != nil {
		return err
	}
	// Decode once up front: a seed that parses but cannot decode would be
	// stored happily and only fail on the first copy.
	if _, err := DecodeSecret(seed); err != nil {
		return err
	}

	name := strings.TrimSpace(a.Values["name"])
	if name == "" {
		// An otpauth URI names the account it belongs to; using it means a
		// pasted URI needs no other input at all.
		name = strings.TrimSpace(account)
	}
	if name == "" {
		name = strings.TrimSpace(issuer)
	}
	if name == "" {
		return errors.New("totp: a name is required")
	}

	idx, err := LoadIndex(d.IndexPath)
	if err != nil {
		return err
	}
	if idx.Backend == "" {
		return errors.New("totp: no secrets backend configured yet")
	}
	if _, ok := idx.Find(name); ok {
		return fmt.Errorf("totp: an entry named %q already exists", name)
	}
	store, err := d.OpenStore(idx.Backend)
	if err != nil {
		return err
	}

	entry := Entry{
		Name:      name,
		Issuer:    issuer,
		Digits:    params.Digits,
		Period:    params.Period,
		Algorithm: params.Algorithm,
		Created:   d.Now().UTC(),
	}
	cred := secrets.Credential{Password: a.Values["credential"]}
	// The seed write and the metadata write are kept apart so a failure can be
	// attributed to the right thing: only store.Set can indict the backend,
	// while idx.Add and SaveIndex fail for local, disk-shaped reasons a setup
	// wizard has nothing useful to say about.
	storeSeed := func(ctx context.Context) error {
		if err := store.Set(ctx, secretKey(name), seed, cred); err != nil {
			return err
		}
		// A completed write proves the backend works, so any recorded failure
		// is stale — cleared here rather than after the index write for the
		// same reason handleCopy clears before decoding.
		d.State.Clear()
		return nil
	}
	recordEntry := func() error {
		if err := idx.Add(entry); err != nil {
			return err
		}
		return SaveIndex(d.IndexPath, idx)
	}

	if store.Blocking() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), addTimeout)
			defer cancel()
			if err := storeSeed(ctx); err != nil {
				// The wrapped message names the entry, never the seed: it ends
				// up on screen as the wizard's status subtitle and lives for the
				// daemon's lifetime.
				wrapped := fmt.Errorf("could not add %q: %w", name, err)
				if backendUnusable(err) {
					d.reportUnusable(idx.Backend, wrapped)
					return
				}
				d.Notify(wrapped.Error())
				return
			}
			if err := recordEntry(); err != nil {
				d.Notify(fmt.Sprintf("could not add %q: %v", name, err))
				return
			}
			// A toast rather than a reopen: the user asked for one thing and
			// got it, and throwing the launcher back in their face after every
			// add would be noise.
			d.Notify(fmt.Sprintf("added TOTP entry %q", name))
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), addTimeout)
	defer cancel()
	if err := storeSeed(ctx); err != nil {
		// The same routing as the detached path, so "a backend-unusable failure
		// opens the wizard" holds for every backend rather than only the blocking
		// ones. It is gated on the wizard being wired because an unwired caller
		// must keep getting the error back from Dispatch — the wizard is the only
		// other place this failure would be reported.
		if d.wizardWired() && backendUnusable(err) {
			d.reportUnusable(idx.Backend, fmt.Errorf("could not add %q: %w", name, err))
			return nil
		}
		return err
	}
	return recordEntry()
}

// probeBackend proves a backend can actually hold a seed, by writing a
// throwaway value and deleting it again. probe false is the backends that need
// no proving (a plaintext file is a plain disk write), and makes this a no-op so
// every caller can run the same sequence.
//
// The two failures are worded apart because they are different diagnoses: a
// refused write is an unusable keyring, while a write that lands and will not
// come back out is a keyring that would silently accumulate every seed the user
// ever adds.
func (d Deps) probeBackend(ctx context.Context, store secrets.Store, probe bool) error {
	if !probe {
		return nil
	}
	if err := store.Set(ctx, probeKey, "probe", secrets.Credential{}); err != nil {
		return fmt.Errorf("the OS keyring is not usable: %w", err)
	}
	if err := store.Delete(ctx, probeKey, secrets.Credential{}); err != nil {
		return fmt.Errorf("the OS keyring stored a test secret but would not remove it: %w", err)
	}
	return nil
}

// persistBackend records store as this machine's choice, re-reading the index
// first so a concurrent add is not clobbered by a stale copy.
//
// It is kept apart from probeBackend because the two fail for entirely different
// reasons: a failed probe is the definition of an unusable backend and belongs
// in the wizard, while a failed index write is a disk problem that retrying a
// keyring probe would never fix.
func (d Deps) persistBackend(store secrets.Store) error {
	idx, err := LoadIndex(d.IndexPath)
	if err != nil {
		return err
	}
	idx.Backend = store.Name()
	return SaveIndex(d.IndexPath, idx)
}

// finishSetup is the asynchronous tail every path that ends in "this backend is
// now the one" shares: probe it, retire any recorded failure, persist the
// choice, and put the user back in the launcher. It is one function because
// handleSetup's detached branch and handleWizardFix's auto-retry must agree
// exactly on that order and on what each failure is reported as — a wizard fix
// that worked has to retire the wizard the same way a manual retry does.
//
// It never spawns a goroutine: both callers already run detached and own their
// own context, and a function that sometimes detached would make "has this
// finished" unanswerable at the call site.
func (d Deps) finishSetup(ctx context.Context, backend string, store secrets.Store, probe bool) {
	if err := d.probeBackend(ctx, store, probe); err != nil {
		// Every probe failure routes to the wizard, without consulting
		// backendUnusable: the probe's whole job is to answer "can this backend
		// hold seeds", so a probe that could not finish — for any reason,
		// including an unlock prompt nobody answered — has answered no, and
		// retrying is the next step either way.
		d.reportUnusable(backend, err)
		return
	}
	// Cleared here, before the index write, for the reason handleCopy and
	// handleAdd clear before their own bookkeeping: the backend has just
	// answered for itself, so a recorded failure against it is disproved now.
	// Clearing after persist instead would leave a wizard on screen diagnosing a
	// keyring that demonstrably works, every time the disk refused the write.
	d.State.Clear()
	if err := d.persistBackend(store); err != nil {
		d.Notify(err.Error())
		return
	}
	if d.Reopen != nil {
		// Reopening is the confirmation: the user lands on the normal entry/add
		// rows, which both prove the backend took and offer the obvious next
		// step. A toast on top would say it twice.
		d.Reopen(reopenQuery)
		return
	}
	d.Notify(fmt.Sprintf("TOTP secrets will be stored in the %s backend", store.Name()))
}

// handleWizardFix runs one canned repair from wizardFixes and, when it works,
// finishes setup so the wizard retires itself — the user pressed Enter on
// "start the daemon", and being handed a "now press Retry" row afterwards would
// be the launcher asking them to do its remaining half of the job.
//
// The command is looked up, never taken from the Action: Argv is [backend,
// fixID] and anything the table does not name is refused outright, so a forged
// Result cannot turn this kind into arbitrary execution. The lookup and the
// store open are synchronous so a bad key is a real dispatcher error; the run
// itself is detached because a systemd call takes as long as it takes and the
// handler runs on the GTK main loop (see handleCopy).
//
// A command that fails routes to the wizard rather than a toast, carrying its
// own output: the user is looking at the wizard, and "could not start the Secret
// Service daemon: Unit not found" is the sentence that tells them the next row
// down — install it — is the one they want.
func (d Deps) handleWizardFix(a providers.Action) error {
	if len(a.Argv) < 2 {
		return errors.New("totp-wizard-fix: expected a backend and a fix id")
	}
	backend := strings.TrimSpace(a.Argv[0])
	id := strings.TrimSpace(a.Argv[1])
	fix, ok := lookupWizardFix(backend, id)
	if !ok {
		return fmt.Errorf("totp-wizard-fix: no fix %q for the %s backend", id, backend)
	}
	store, err := d.OpenStore(backend)
	if err != nil {
		return err
	}
	// The password a stdin fix pipes to the command. Reading it here keeps the
	// Action out of the goroutine below; a fix without stdin ignores Values
	// entirely, and a nil Values map reads as the empty password — a
	// legitimate keyring choice, not an error.
	var input string
	if fix.stdin && a.Values != nil {
		input = a.Values[wizardPasswordKey]
	}

	go func() {
		fixCtx, cancelFix := context.WithTimeout(context.Background(), fixTimeout)
		var out []byte
		var err error
		if fix.stdin {
			out, err = d.RunInput(fixCtx, fix.argv, input)
		} else {
			out, err = d.Run(fixCtx, fix.argv)
		}
		cancelFix()
		if err != nil {
			d.reportUnusable(backend, fmt.Errorf("%s: %w%s", fix.failMsg, err, trimmedOutput(out)))
			return
		}
		// A budget of its own, not what the fix left over: see fixTimeout. It is
		// deliberately the same probeTimeout handleSetup gives the manual Retry
		// row, because finishSetup's contract is that the two paths behave
		// identically — a slow repair that worked must not be reported as a
		// broken backend.
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		d.finishSetup(ctx, backend, store, backend == secrets.BackendKeyring)
	}()
	return nil
}

// handleSetup records which secrets backend holds this machine's seeds.
//
// A keyring is probed with a throwaway write and delete before being
// persisted: go-keyring reports success against Secret Service
// implementations that then refuse to store anything, and discovering that
// after the user has added a code would lose the seed. A probe failure leaves
// the index untouched, so the setup rows come back on the next query.
//
// The probe is exactly the moment a locked collection is most likely to put up
// an unlock prompt — it is the first thing this feature ever asks of the
// keyring — so a Blocking backend is probed on its own goroutine and reports
// through Notify. Running it on the GTK main loop would freeze the daemon for
// as long as the user took to answer (handleAdd carries the same split).
func (d Deps) handleSetup(a providers.Action) error {
	if len(a.Argv) == 0 || strings.TrimSpace(a.Argv[0]) == "" {
		return errors.New("totp-setup: no backend name")
	}
	backend := strings.TrimSpace(a.Argv[0])
	if backend == secrets.BackendNimbus {
		return errors.New("Nimbus is not available yet")
	}
	store, err := d.OpenStore(backend)
	if err != nil {
		return err
	}
	probe := backend == secrets.BackendKeyring

	if store.Blocking() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			d.finishSetup(ctx, backend, store, probe)
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if err := d.probeBackend(ctx, store, probe); err != nil {
		return err
	}
	// The synchronous path is the wizard's retry row too, for a backend whose
	// calls never block (a plaintext store that failed a read is diagnosed by the
	// same wizard). Without the clear and the reopen, retry would hide the window
	// and do nothing visible, and the wizard could only ever be retired by a later
	// successful copy or add — the one escape it offers would not work. A failed
	// persist below still reaches the user, as the dispatcher's own error.
	d.State.Clear()
	if err := d.persistBackend(store); err != nil {
		return err
	}
	if d.Reopen != nil {
		d.Reopen(reopenQuery)
	}
	return nil
}
