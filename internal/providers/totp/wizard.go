package totp

import (
	"fmt"
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

// Scores for the wizard block. They sit above TriggerScore so a wizard, once
// triggered, owns the top of the list, and they are fixed constants because the
// rows must keep a diagnosis → fix → retry → escape order that no fuzzy score
// may reshuffle.
const (
	// wizardStatusScore pins the header (what actually failed) first.
	wizardStatusScore = 860
	// wizardGuidanceFirstScore is the first fix row's score; each subsequent
	// row drops by wizardGuidanceStep. There is room for exactly three rows
	// between it and wizardRetryScore, which is the cap on a backend's
	// wizardGuidance table.
	wizardGuidanceFirstScore = 850
	wizardGuidanceStep       = 10
	// wizardRetryScore keeps "try again" under the fixes: retrying before
	// reading them just reproduces the failure.
	wizardRetryScore = 820
	// wizardBackScore puts the escape hatch last — it is the rarest choice,
	// and only offered at all during first-time setup.
	wizardBackScore = 810
)

// Icon theme names used by the wizard rows.
const (
	iconWizardStatus   = "dialog-warning-symbolic"
	iconWizardGuidance = "edit-copy-symbolic"
	iconWizardRetry    = "view-refresh-symbolic"
	iconWizardBack     = "go-previous-symbolic"
)

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
}

// Fail records that backend failed with err, replacing any previous failure —
// the newest diagnosis is the only one worth showing. A nil err is a no-op, so
// callers can hand it the result of an operation without branching first.
func (s *SetupState) Fail(backend string, err error) {
	if s == nil || err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backend = backend
	s.message = err.Error()
	s.active = true
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
}

// Snapshot returns the recorded failure. ok is false when nothing is recorded —
// including on a nil receiver, so an unwired front-end always takes the
// no-wizard path.
func (s *SetupState) Snapshot() (backend, message string, ok bool) {
	if s == nil {
		return "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backend, s.message, s.active
}

// wizardApplies reports whether a failure recorded against the failed backend
// still describes the backend the user would be using, given the one persisted
// in the index. An empty persisted name is first-time setup, where every
// candidate failure applies; a different name means the user reconfigured out
// of band and the recorded diagnosis is stale.
//
// It is the one place this rule lives: the provider consults it to decide
// whether to render the wizard, and the handlers consult it before recording a
// failure, so a failure that would be discarded is reported as a toast instead
// of vanishing.
func wizardApplies(persisted, failed string) bool {
	return persisted == "" || persisted == failed
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
// new Result fields and no change in internal/ui at all: each row dispatches
// through an action kind that already exists (a copy for the fix rows, the
// setup action for retry, the reset action for the escape hatch). Errors show
// up in the launcher, where the user is looking and can act on them, instead of
// as a desktop toast that vanishes into a dead end.
//
// message is the recorded error text and rides the header row's subtitle: it is
// the machine's own account of the failure, while the guidance rows below are
// distro-neutral best-effort advice.
//
// firstTime reports that no backend has been persisted yet, which is the only
// situation where "choose a different backend" makes sense — once seeds exist
// under a backend, switching is not a wizard's decision.
func wizardResults(backend, message string, firstTime bool) []providers.Result {
	guidance := wizardGuidance(backend)
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
		out = append(out, providers.Result{
			ID:       "totp:wizard:" + g.id,
			Title:    g.title,
			Subtitle: g.subtitle,
			Icon:     providers.Icon{ThemeName: iconWizardGuidance},
			Category: providers.CatTOTP,
			Score:    wizardGuidanceFirstScore - i*wizardGuidanceStep,
			// Copying beats running: banshee has no business executing a
			// package-manager or systemd command on the user's behalf, and the
			// user's terminal is one paste away.
			Action: providers.Action{Kind: providers.ActClipboardCopy, Text: g.cmd},
		})
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
	if firstTime {
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

// guidanceRow is one "here is how you fix it" row before it becomes a Result:
// a human instruction plus the exact command that lands on the clipboard.
type guidanceRow struct {
	// id is the suffix appended to "totp:wizard:", kept backend-qualified so
	// two backends' advice can never collide on a result ID.
	id       string
	title    string
	subtitle string
	cmd      string
}

// wizardGuidance returns the fix advice for backend, most-likely cause first.
//
// Only the keyring has advice today: it is the one backend with an external
// dependency that can be absent (a Secret Service provider on a live session
// bus). A backend with nothing useful to say returns nil and its wizard is
// header plus retry, which is still strictly better than a toast — the failure
// stays on screen and is one keypress from being retried. This is the seam a
// future remote backend fills in.
//
// A backend's table may hold at most three rows; see wizardGuidanceFirstScore.
func wizardGuidance(backend string) []guidanceRow {
	switch backend {
	case secrets.BackendKeyring:
		return []guidanceRow{
			{
				id:       "keyring:daemon",
				title:    "Start the Secret Service daemon",
				subtitle: "Enter copies: systemctl --user start gnome-keyring-daemon",
				cmd:      "systemctl --user start gnome-keyring-daemon",
			},
			{
				id:       "keyring:install",
				title:    "Install a Secret Service provider",
				subtitle: "Enter copies: gnome-keyring — install with your package manager",
				cmd:      "gnome-keyring",
			},
			{
				id:       "keyring:dbus",
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
