package totp

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// ActTOTPWizardReset abandons a first-time backend setup and returns the user
// to the backend chooser: the handler in handlers.go clears the shared
// SetupState and reopens the launcher on the trigger, so the next Query renders
// the ordinary setup rows again. It is declared here beside the wizard rows
// that emit it, so this file compiles without waiting on the handler.
const ActTOTPWizardReset = "totp-wizard-reset"

// ActTOTPWizardFix applies one canned repair for an unusable backend and, when
// it succeeds, retries setup so the wizard retires itself.
//
// Argv is [backend, fixID] and is a *lookup key*, never a command line: the
// handler resolves the pair through wizardFixes and refuses anything the table
// does not name, so a forged Result — a rogue plugin, a hand-built IPC payload —
// can never turn this kind into "run arbitrary argv". It is declared here beside
// the rows that emit it for the same reason as ActTOTPWizardReset.
const ActTOTPWizardFix = "totp-wizard-fix"

// Scores for the wizard block. They sit above TriggerScore so a wizard, once
// triggered, owns the top of the list, and they are fixed constants because the
// rows must keep a diagnosis → fix → retry → escape order that no fuzzy score
// may reshuffle.
const (
	// wizardStatusScore pins the header (what actually failed) first.
	wizardStatusScore = 870
	// wizardGuidanceFirstScore is the first fix row's score; each subsequent
	// row drops by wizardGuidanceStep. There is room for exactly four rows
	// between it and wizardRetryScore, which is the cap on a backend's
	// wizardGuidance table.
	wizardGuidanceFirstScore = 860
	wizardGuidanceStep       = 10
	// wizardRetryScore keeps "try again" under the fixes: retrying before
	// reading them just reproduces the failure.
	wizardRetryScore = 820
	// wizardBackScore puts the escape hatch last — it is the rarest choice,
	// and only offered at all for a manager the user is not relying on yet
	// (see wizardResults' offerBack).
	wizardBackScore = 810
)

// Icon theme names used by the wizard rows. The three guidance icons exist
// because a guidance row no longer does one thing: the icon is the only advance
// warning the user gets that Enter will copy text, run something, or throw a
// terminal at them.
const (
	iconWizardStatus   = "dialog-warning-symbolic"
	iconWizardCopy     = "edit-copy-symbolic"
	iconWizardRun      = "media-playback-start-symbolic"
	iconWizardPassword = "dialog-password-symbolic"
	iconWizardTerminal = "utilities-terminal-symbolic"
	iconWizardRetry    = "view-refresh-symbolic"
	iconWizardBack     = "go-previous-symbolic"
)

// wizardPasswordKey is the form-field key a password-collecting fix row submits
// under, and the Action.Values key the fix handler pipes to the command's
// stdin. One constant because the two sides never meet in the type system.
const wizardPasswordKey = "password"

// errPasswordMismatch rejects a create-keyring form whose two password fields
// differ. By the time Build runs the launcher has already hidden the window, so
// this surfaces as a notification rather than an in-form error — the same
// accepted limitation the add form documents.
var errPasswordMismatch = errors.New("the two passwords do not match")

// SetupState records the most recent "this backend is not usable" failure so
// the provider can render a setup wizard instead of rows the user cannot use.
//
// It is deliberately ephemeral and daemon-lifetime: a restart re-probes the
// backend from scratch rather than resurrecting a stale diagnosis, which is the
// right default when the usual fix (start the keyring daemon, install a Secret
// Service provider) happens outside banshee. Any successful backend operation
// clears it, so the wizard disappears the moment the backend works again.
//
// The provider (which renders the wizard rows, off the GTK main loop) and the
// action handlers (which record failures, from their own detached goroutines)
// share one instance, created and owned by boot — hence the mutex. Every method
// is safe on a nil receiver so a front-end that never wires one up simply gets
// the feature switched off instead of a panic.
//
// It never holds secret material: Fail stores err.Error(), an immutable string,
// rather than the error value, so no error is shared across goroutines and no
// secret-bearing wrapped value stays reachable from daemon-lifetime state.
type SetupState struct {
	mu      sync.Mutex
	backend string
	message string
	active  bool
	// fromSetup marks the recorded failure as raised by an explicit setup or
	// repair attempt rather than by ordinary use of a configured manager. The
	// provider's gate needs the distinction because the two look identical
	// otherwise: a failure naming a manager that is not configured is either a
	// stale diagnosis to discard, or the result of the user trying to configure
	// that very manager, which is the report they are waiting for.
	fromSetup bool
}

// Fail records that backend failed with err, replacing any previous failure —
// the newest diagnosis is the only one worth showing. A nil err is a no-op, so
// callers can hand it the result of an operation without branching first.
//
// It is the ordinary-use variant: the failure is only rendered while backend is
// still one of the configured managers. Use FailSetup for a failure raised by an
// attempt to configure or repair one.
func (s *SetupState) Fail(backend string, err error) {
	s.fail(backend, err, false)
}

// FailSetup is Fail for a failure raised by an explicit setup or repair attempt —
// a probe of a manager the user just picked, a wizard fix that did not take. The
// recorded failure is rendered even though the manager is not configured yet,
// because configuring it is precisely what failed.
func (s *SetupState) FailSetup(backend string, err error) {
	s.fail(backend, err, true)
}

// fail is the shared body of Fail and FailSetup.
func (s *SetupState) fail(backend string, err error, fromSetup bool) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = backend
	s.message = err.Error()
	s.active = true
	s.fromSetup = fromSetup
}

// Clear forgets the recorded failure. Callers invoke it on any successful
// backend operation, which is what makes the wizard vanish without the user
// dismissing it.
func (s *SetupState) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = ""
	s.message = ""
	s.active = false
	s.fromSetup = false
}

// Snapshot returns the recorded failure and whether it came from an explicit
// setup attempt. ok is false when nothing is recorded — including on a nil
// receiver, so an unwired front-end always takes the no-wizard path.
//
// All four values come out under one lock rather than from separate accessors:
// the gate that reads them decides on the combination, and a torn read could pair
// one failure's backend with another's setup flag.
func (s *SetupState) Snapshot() (backend, message string, fromSetup, ok bool) {
	if s == nil {
		return "", "", false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backend, s.message, s.fromSetup, s.active
}

// wizardApplies reports whether a failure recorded against the failed backend
// still describes a manager the user would be using, given the ones configured
// in the index. Nothing configured is first-time setup, where every candidate
// failure applies; a name that is not among them means the user reconfigured out
// of band and the recorded diagnosis is stale.
//
// It deliberately says nothing about setup attempts: a failure raised while
// configuring a *new* manager names one that is not configured yet and would be
// discarded here, which is why the callers treat a setup-flagged failure as
// applying regardless (see SetupState.fromSetup).
//
// It is the one place this rule lives: the provider consults it to decide
// whether to render the wizard, and the handlers consult it before recording a
// failure, so a failure that would be discarded is reported as a toast instead
// of vanishing.
func wizardApplies(configured []string, failed string) bool {
	return len(configured) == 0 || slices.Contains(configured, failed)
}

// WithSetupState injects the shared failure state. Without it the provider
// never renders a wizard, which is exactly what a headless caller or a test
// that only exercises the entry rows wants.
func WithSetupState(s *SetupState) Option {
	return func(p *Provider) {
		p.setup = s
	}
}

// wizardResults renders the setup wizard for an unusable backend: what failed,
// how to fix it, and how to get back out.
//
// # Wizard as result rows
//
// The wizard is plain result rows rather than a new UI surface. Every choice a
// user has to make here is "pick one of these things" — which is what a
// launcher list already is — so the wizard needs no new form-field kinds, no
// new Result fields and no change in internal/ui at all: each row is an ordinary
// Result dispatching an ordinary action (a clipboard copy, a terminal handoff,
// the fix action, the setup action for retry, the reset action for the escape
// hatch). Errors show up in the launcher, where the user is looking and can act
// on them, instead of as a desktop toast that vanishes into a dead end.
//
// message is the recorded error text and rides the header row's subtitle: it is
// the machine's own account of the failure, while the guidance rows below are
// distro-neutral best-effort advice.
//
// offerBack asks for the "choose a different backend" escape row. It belongs on
// a wizard the user can walk away from: first-time setup, where nothing has been
// persisted yet, and a failed attempt to add a further manager, where the working
// ones are still there to go back to. It is withheld only when the failing
// manager is one the user already keeps seeds in — switching that is not a
// wizard's decision — and withholding it anywhere else would leave the trigger
// rendering the wizard with no way out, since this row is what clears the
// recorded failure.
//
// lookPath probes for a program on PATH (exec.LookPath in production) and is
// threaded through rather than called directly so the rendered rows stay a pure
// function of their inputs, which is what lets a test pin them byte for byte on
// any machine.
func wizardResults(backend, message string, offerBack bool, lookPath func(string) (string, error)) []providers.Result {
	guidance := wizardGuidance(backend, lookPath)
	out := make([]providers.Result, 0, len(guidance)+3)
	out = append(out, providers.Result{
		ID:       "totp:wizard:status",
		Title:    wizardHeader(backend),
		Subtitle: message,
		Icon:     providers.Icon{ThemeName: iconWizardStatus},
		Category: providers.CatTOTP,
		Score:    wizardStatusScore,
		// The header retries too. A zero Action would make activating the row
		// fail with a dispatcher error toast, and "run it again" is the least
		// surprising thing for the row the user's cursor starts on to do.
		Action: providers.Action{Kind: ActTOTPSetup, Argv: []string{backend}},
	})
	for i, g := range guidance {
		row := providers.Result{
			ID:       "totp:wizard:" + g.id,
			Title:    g.title,
			Subtitle: g.subtitle,
			Icon:     providers.Icon{ThemeName: g.icon()},
			Category: providers.CatTOTP,
			Score:    wizardGuidanceFirstScore - i*wizardGuidanceStep,
		}
		// Three tiers, by what the fix costs the user to authorize:
		// an unprivileged, user-level repair (starting a systemd --user
		// unit) banshee runs itself, because making the user paste a
		// command banshee could have run is busywork; a privileged one
		// (installing a package) is handed to the user's terminal, where
		// sudo can prompt interactively and banshee never sees the
		// password; a pure diagnostic is copied, because the answer only
		// means anything in the user's own shell. A repair that needs a
		// secret from the user (creating the login keyring) opens a masked
		// form first — the form replaces the primary activation, so such a
		// row carries no direct Action at all and nothing can dispatch it
		// without the password step.
		if form := g.formFor(backend); form != nil {
			row.Form = form
		} else {
			row.Action = g.action(backend)
		}
		out = append(out, row)
	}
	out = append(out, providers.Result{
		ID:       "totp:wizard:retry",
		Title:    wizardRetryTitle(backend),
		Subtitle: wizardRetrySubtitle(backend),
		Icon:     providers.Icon{ThemeName: iconWizardRetry},
		Category: providers.CatTOTP,
		Score:    wizardRetryScore,
		Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{backend}},
	})
	if offerBack {
		out = append(out, providers.Result{
			ID:       "totp:wizard:back",
			Title:    "Choose a different backend",
			Subtitle: "Return to the backend choices",
			Icon:     providers.Icon{ThemeName: iconWizardBack},
			Category: providers.CatTOTP,
			Score:    wizardBackScore,
			Action:   providers.Action{Kind: ActTOTPWizardReset},
		})
	}
	return out
}

// guidanceKind says what pressing Enter on a guidance row does, which is the
// one thing that varies between them; everything else about a row is text.
type guidanceKind int

const (
	// guidanceCopy puts cmd on the clipboard and does nothing else. It is the
	// zero value, so a row that forgets to say what it is stays harmless.
	guidanceCopy guidanceKind = iota
	// guidanceRun asks the fix handler to run this row's entry in wizardFixes
	// and retry setup afterwards.
	guidanceRun
	// guidanceTerminal opens the user's terminal on argv, for a command that
	// needs to talk to the user (interactive sudo) rather than just succeed.
	guidanceTerminal
)

// guidanceRow is one "here is how you fix it" row before it becomes a Result:
// a human instruction plus whatever the row's kind needs to act on.
type guidanceRow struct {
	// id is the suffix appended to "totp:wizard:", kept backend-qualified so
	// two backends' advice can never collide on a result ID. For a guidanceRun
	// row it doubles as the key into the backend's wizardFixes table.
	id       string
	kind     guidanceKind
	title    string
	subtitle string
	// cmd is the text a guidanceCopy row copies. Other kinds leave it empty:
	// their command lives in wizardFixes (run) or in argv (terminal), and a
	// second copy of it here would be a second thing to keep in sync.
	cmd string
	// argv is the command a guidanceTerminal row hands to the terminal.
	argv []string
	// passwordForm makes a guidanceRun row collect a password first: instead
	// of dispatching immediately, the row opens a masked in-launcher form and
	// the submitted password rides Action.Values to the fix handler, which
	// pipes it to the command's stdin (never argv). Only meaningful on
	// guidanceRun rows whose wizardFixes entry sets stdin.
	passwordForm bool
}

// action builds the Action this row dispatches.
//
// A run row deliberately carries only [backend, id] — the key, not the command.
// Actions travel through the dispatcher from anywhere a Result can be minted, so
// putting argv on one would make "execute this argv" a kind any forged Result
// could reach; the handler instead looks the pair up in wizardFixes, which this
// package writes and nothing else can extend.
//
// A terminal row may carry argv because ActTerminal is a pre-existing surface
// with exactly that contract (it is how lastaction re-attaches a session), and
// because the argv here is built from static package-level strings — no user
// input, no secret material, ever reaches it.
func (g guidanceRow) action(backend string) providers.Action {
	switch g.kind {
	case guidanceRun:
		return providers.Action{Kind: ActTOTPWizardFix, Argv: []string{backend, g.id}}
	case guidanceTerminal:
		return providers.Action{Kind: providers.ActTerminal, Argv: g.argv}
	default:
		return providers.Action{Kind: providers.ActClipboardCopy, Text: g.cmd}
	}
}

// icon returns the theme icon matching the row's kind, so the list telegraphs
// what Enter will do before the user presses it.
func (g guidanceRow) icon() string {
	switch {
	case g.passwordForm:
		return iconWizardPassword
	case g.kind == guidanceRun:
		return iconWizardRun
	case g.kind == guidanceTerminal:
		return iconWizardTerminal
	default:
		return iconWizardCopy
	}
}

// formFor builds the masked password form a passwordForm row opens instead of
// dispatching. Nil for every other row, which is what keeps them dispatching
// directly.
//
// Two fields, neither required: an empty password is a legitimate choice
// (gnome-keyring stores such a keyring unencrypted, which is what an autologin
// user who never types a password may actually want), so the only validation
// is that the two entries agree. The launcher trims surrounding whitespace
// from every submitted value — an existing form-contract behavior — so a
// password that begins or ends with a space cannot be set here; that is the
// same trade every consumer of the form system makes.
//
// The submitted Values map carries only the password, under wizardPasswordKey:
// the confirmation is a UI-side check and has no business travelling further.
func (g guidanceRow) formFor(backend string) *providers.Form {
	if !g.passwordForm {
		return nil
	}
	id := g.id
	return &providers.Form{
		Title: g.title,
		Fields: []providers.FormField{
			{
				Key:         wizardPasswordKey,
				Label:       "Keyring password",
				Placeholder: "leave empty to store the keyring unencrypted",
				Secret:      true,
			},
			{
				Key:         "confirm",
				Label:       "Repeat password",
				Placeholder: "same password again",
				Secret:      true,
			},
		},
		Build: func(values map[string]string) (providers.Action, error) {
			if values[wizardPasswordKey] != values["confirm"] {
				return providers.Action{}, errPasswordMismatch
			}
			return providers.Action{
				Kind:   ActTOTPWizardFix,
				Argv:   []string{backend, id},
				Values: map[string]string{wizardPasswordKey: values[wizardPasswordKey]},
			}, nil
		},
	}
}

// wizardFix is one repair banshee is willing to run itself: the exact argv and
// the sentence that introduces a failure to the user.
type wizardFix struct {
	// argv is executed directly, without a shell, so nothing here is ever
	// word-split or expanded.
	argv []string
	// failMsg heads the error the wizard re-renders with when argv fails, so
	// the user reads "could not start the Secret Service daemon: …" rather than
	// a bare exit status.
	failMsg string
	// stdin makes the handler pipe the submitted password (Action.Values under
	// wizardPasswordKey) to the command's standard input. It is the one
	// exception to "user-level and non-interactive": the command still runs
	// without a terminal, but it reads exactly one secret from stdin — the
	// same channel the clipboard and the secret stores use, because a password
	// on argv would sit in the process list.
	stdin bool
	// then is an optional second command run after argv succeeds, sharing its
	// failMsg on failure. It never receives stdin: a follow-up is plumbing
	// (reloading a daemon), not another secret entry.
	then []string
}

// wizardFixes is the complete set of commands ActTOTPWizardFix may run, keyed
// by backend and then by the guidance row's id. The handler executes nothing
// outside it, which is what makes the action kind safe to expose.
//
// Everything here must be user-level and non-interactive: it runs detached from
// a handler goroutine with no terminal attached, so a command that wants a
// password or a confirmation would hang until the timeout and report a
// meaningless failure. Anything privileged belongs in a guidanceTerminal row,
// where the user's own terminal can answer sudo.
var wizardFixes = map[string]map[string]wizardFix{
	secrets.BackendKeyring: {
		"keyring:daemon": {
			argv:    []string{"systemctl", "--user", "start", "gnome-keyring-daemon"},
			failMsg: "could not start the Secret Service daemon",
		},
		// --unlock reads the password from stdin and creates the login keyring
		// when none exists — the "daemon running, zero collections" state a
		// machine lands in when PAM never provisioned one (autologin, greetd,
		// tty logins). Correctness does not hinge on this command's exit
		// status: the setup probe that follows is what actually proves the
		// keyring took the password and can store secrets.
		//
		// The restart afterwards is load-bearing: with a socket-activated
		// daemon already running, --unlock spawns a second instance that
		// writes login.keyring to disk without the running daemon ever
		// loading it — SetAlias then fails with "collection does not exist"
		// and the probe keeps failing against an empty Secret Service. A
		// restart makes the daemon load the new collection and its default
		// alias (verified against gnome-keyring 50: collection appears,
		// aliased, unlocked).
		"keyring:create": {
			argv:    []string{"gnome-keyring-daemon", "--unlock"},
			then:    []string{"systemctl", "--user", "restart", "gnome-keyring-daemon"},
			failMsg: "could not create or unlock the login keyring",
			stdin:   true,
		},
	},
}

// lookupWizardFix resolves an ActTOTPWizardFix Argv pair. ok is false for an
// unknown backend or an id that backend does not define — including an id that
// another backend does, so the two halves of the key can never be mixed.
func lookupWizardFix(backend, id string) (wizardFix, bool) {
	fixes, ok := wizardFixes[backend]
	if !ok {
		return wizardFix{}, false
	}
	fix, ok := fixes[id]
	return fix, ok
}

// packageManagers maps a package manager's binary onto the command that
// installs gnome-keyring with it, in probe order — the first one found on PATH
// wins, because a machine with two of them (a distro shipping both dnf and
// zypper, a user with apt on an Arch box) is still overwhelmingly likely to be
// administered with the first.
//
// The list is deliberately short: it covers the mainstream families and a
// machine outside them falls back to the copy row, which was the only behavior
// before and is never wrong.
var packageManagers = []struct {
	bin string
	cmd string
}{
	{bin: "pacman", cmd: "sudo pacman -S --needed gnome-keyring"},
	{bin: "apt", cmd: "sudo apt install gnome-keyring"},
	{bin: "dnf", cmd: "sudo dnf install gnome-keyring"},
	{bin: "zypper", cmd: "sudo zypper install gnome-keyring"},
}

// installGuidance builds the "install a Secret Service provider" row for this
// machine: a terminal handoff when a known package manager is on PATH, and
// otherwise the distro-neutral copy row, unchanged from before this probe
// existed. A nil lookPath counts as "found nothing", so a caller that never
// wired one up gets the safe fallback rather than a panic.
func installGuidance(lookPath func(string) (string, error)) guidanceRow {
	row := guidanceRow{
		id:       "keyring:install",
		title:    "Install a Secret Service provider",
		subtitle: "Enter copies: gnome-keyring — install with your package manager",
		cmd:      "gnome-keyring",
	}
	if lookPath == nil {
		return row
	}
	for _, m := range packageManagers {
		if _, err := lookPath(m.bin); err != nil {
			continue
		}
		return guidanceRow{
			id:       "keyring:install",
			kind:     guidanceTerminal,
			title:    "Install a Secret Service provider",
			subtitle: "Enter opens a terminal to install it (sudo prompts there)",
			argv:     terminalWrap(m.cmd),
		}
	}
	return row
}

// terminalWrap turns an install command into argv for ActTerminal, holding the
// window open when it finishes.
//
// Every terminal banshee resolves is launched as `term -e argv…` and closes the
// moment argv exits, which would flash a package manager's output — including
// the reason it refused — past the user in a fraction of a second. The trailing
// prompt keeps the window up until they have read it.
//
// Joining cmd into a shell string is safe because every cmd is a static literal
// from packageManagers: no user input, no path, nothing quoted is reachable
// here. Do not extend this to caller-supplied text without quoting it.
func terminalWrap(cmd string) []string {
	return []string{"sh", "-c", cmd + `; printf '\nPress Enter to close\n'; read _`}
}

// wizardGuidance returns the fix advice for backend, most-likely cause first.
// lookPath is the PATH probe installGuidance uses to decide whether it can
// offer a real install command.
//
// Only the keyring has advice today: it is the one backend with an external
// dependency that can be absent (a Secret Service provider on a live session
// bus). A backend with nothing useful to say returns nil and its wizard is
// header plus retry, which is still strictly better than a toast — the failure
// stays on screen and is one keypress from being retried. This is the seam a
// future remote backend fills in.
//
// A backend's table may hold at most four rows; see wizardGuidanceFirstScore.
func wizardGuidance(backend string, lookPath func(string) (string, error)) []guidanceRow {
	switch backend {
	case secrets.BackendKeyring:
		return []guidanceRow{
			{
				id:       "keyring:daemon",
				kind:     guidanceRun,
				title:    "Start the Secret Service daemon",
				subtitle: "Enter starts the daemon and retries setup",
			},
			{
				// The daemon can be up with no keyring behind it: PAM creates
				// the login collection at login, so autologin/greetd/tty
				// sessions never get one and every write fails against an
				// empty Secret Service. Creating (or unlocking) it needs a
				// password, hence the masked form instead of a bare run row.
				id:           "keyring:create",
				kind:         guidanceRun,
				passwordForm: true,
				title:        "Create or unlock the login keyring",
				subtitle:     "Enter asks for a keyring password, then retries setup",
			},
			installGuidance(lookPath),
			{
				id:   "keyring:dbus",
				kind: guidanceCopy,
				// Stays a copy even though banshee could run it: the daemon's
				// environment is not the user's shell environment, so the value
				// banshee would read says nothing about why *their* session has
				// no bus. The answer is only meaningful pasted into the shell
				// the keyring client actually runs in.
				title:    "Check the session bus",
				subtitle: "Enter copies: echo $DBUS_SESSION_BUS_ADDRESS — empty means no session bus",
				cmd:      "echo $DBUS_SESSION_BUS_ADDRESS",
			},
		}
	default:
		return nil
	}
}

// wizardHeader names the failure in the user's terms. The fallback prints the
// raw backend name because an unknown backend is by definition one this build
// has no phrasing for, and naming it is still more useful than "a backend".
func wizardHeader(backend string) string {
	switch backend {
	case secrets.BackendKeyring:
		return "OS keyring is not usable"
	default:
		return fmt.Sprintf("The %s backend is not usable", backend)
	}
}

// wizardRetryTitle names the retry row's action per backend.
func wizardRetryTitle(backend string) string {
	switch backend {
	case secrets.BackendKeyring:
		return "Retry OS keyring setup"
	default:
		return fmt.Sprintf("Retry %s setup", backend)
	}
}

// wizardRetrySubtitle says what retrying actually does, so the user knows
// whether the fix they just applied is the kind this row will notice.
func wizardRetrySubtitle(backend string) string {
	switch backend {
	case secrets.BackendKeyring:
		return "Runs the keyring write/delete probe again"
	default:
		return fmt.Sprintf("Runs the %s setup check again", backend)
	}
}
