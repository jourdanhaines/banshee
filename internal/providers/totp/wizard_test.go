package totp

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// TestWizardApplies pins the staleness rule: a recorded failure is only worth
// rendering while it describes a manager the user would actually be using.
// Nothing configured is first-time setup, where any candidate applies.
//
// It says nothing about setup attempts on purpose — those name a manager that is
// deliberately not configured yet and are carried by SetupState's own flag, which
// is asserted through the provider's gate and the handlers instead.
func TestWizardApplies(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		failed     string
		want       bool
	}{
		{name: "nothing configured admits any failure", configured: nil, failed: "keyring", want: true},
		{name: "nothing configured admits an empty failure", configured: nil, failed: "", want: true},
		{name: "the single configured manager", configured: []string{"keyring"}, failed: "keyring", want: true},
		{name: "another manager is stale", configured: []string{"plaintext"}, failed: "keyring", want: false},
		{name: "the default among several", configured: []string{"keyring", "plaintext"}, failed: "keyring", want: true},
		{name: "a second manager among several", configured: []string{"keyring", "plaintext"}, failed: "plaintext", want: true},
		{name: "a manager configured away", configured: []string{"keyring", "plaintext"}, failed: "nimbus", want: false},
		{name: "an empty failure against a configured manager", configured: []string{"keyring"}, failed: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wizardApplies(tt.configured, tt.failed); got != tt.want {
				t.Fatalf("wizardApplies(%v, %q) = %v, want %v", tt.configured, tt.failed, got, tt.want)
			}
		})
	}
}

func TestSetupStateLifecycle(t *testing.T) {
	tests := []struct {
		name string
		// state builds the state under test and applies the operations; a nil
		// return exercises the nil-receiver path every method must survive.
		state         func() *SetupState
		wantBackend   string
		wantMessage   string
		wantFromSetup bool
		wantOK        bool
	}{
		{
			name:   "zero value has nothing recorded",
			state:  func() *SetupState { return &SetupState{} },
			wantOK: false,
		},
		{
			name: "fail is visible in the snapshot",
			state: func() *SetupState {
				s := &SetupState{}
				s.Fail("keyring", errors.New("no session bus"))
				return s
			},
			wantBackend: "keyring",
			wantMessage: "no session bus",
			wantOK:      true,
		},
		{
			name: "clear forgets the failure",
			state: func() *SetupState {
				s := &SetupState{}
				s.Fail("keyring", errors.New("no session bus"))
				s.Clear()
				return s
			},
			wantOK: false,
		},
		{
			name: "a second failure replaces the first",
			state: func() *SetupState {
				s := &SetupState{}
				s.Fail("keyring", errors.New("no session bus"))
				s.Fail("nimbus", errors.New("not configured"))
				return s
			},
			wantBackend: "nimbus",
			wantMessage: "not configured",
			wantOK:      true,
		},
		{
			name: "a nil error records nothing",
			state: func() *SetupState {
				s := &SetupState{}
				s.Fail("keyring", nil)
				return s
			},
			wantOK: false,
		},
		{
			name: "a nil error leaves an existing failure alone",
			state: func() *SetupState {
				s := &SetupState{}
				s.Fail("keyring", errors.New("no session bus"))
				s.Fail("keyring", nil)
				return s
			},
			wantBackend: "keyring",
			wantMessage: "no session bus",
			wantOK:      true,
		},
		{
			name: "every method is safe on a nil receiver",
			state: func() *SetupState {
				var s *SetupState
				s.Fail("keyring", errors.New("no session bus"))
				s.FailSetup("keyring", errors.New("no session bus"))
				s.Clear()
				return s
			},
			wantOK: false,
		},
		{
			name: "a setup failure is marked as one",
			state: func() *SetupState {
				s := &SetupState{}
				s.FailSetup("keyring", errors.New("collection is locked"))
				return s
			},
			wantBackend:   "keyring",
			wantMessage:   "collection is locked",
			wantFromSetup: true,
			wantOK:        true,
		},
		{
			// The flag belongs to the failure, not to the state: an ordinary
			// failure recorded afterwards must not inherit the wizard-anywhere
			// privilege of the setup attempt it replaced.
			name: "an ordinary failure replacing a setup one clears the mark",
			state: func() *SetupState {
				s := &SetupState{}
				s.FailSetup("keyring", errors.New("collection is locked"))
				s.Fail("plaintext", errors.New("vault is read-only"))
				return s
			},
			wantBackend: "plaintext",
			wantMessage: "vault is read-only",
			wantOK:      true,
		},
		{
			name: "clear forgets the mark too",
			state: func() *SetupState {
				s := &SetupState{}
				s.FailSetup("keyring", errors.New("collection is locked"))
				s.Clear()
				return s
			},
			wantOK: false,
		},
		{
			name: "a nil error records no setup failure either",
			state: func() *SetupState {
				s := &SetupState{}
				s.FailSetup("keyring", nil)
				return s
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, message, fromSetup, ok := tt.state().Snapshot()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if backend != tt.wantBackend {
				t.Errorf("backend = %q, want %q", backend, tt.wantBackend)
			}
			if message != tt.wantMessage {
				t.Errorf("message = %q, want %q", message, tt.wantMessage)
			}
			if fromSetup != tt.wantFromSetup {
				t.Errorf("fromSetup = %v, want %v", fromSetup, tt.wantFromSetup)
			}
		})
	}
}

// TestSetupStateConcurrent interleaves every method from several goroutines.
// The state is written by detached handler goroutines and read by the provider
// off the GTK main loop, so the point is both the race detector and the value
// invariant: a snapshot never reports a failure without the pair of fields that
// go with it.
func TestSetupStateConcurrent(t *testing.T) {
	const goroutines = 8
	const iterations = 200

	s := &SetupState{}
	failure := errors.New("no session bus")

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch g % 3 {
				case 0:
					// Both recorders, alternating, so the setup flag is written
					// under the race detector as well as read.
					if i%2 == 0 {
						s.Fail(secrets.BackendKeyring, failure)
					} else {
						s.FailSetup(secrets.BackendKeyring, failure)
					}
				case 1:
					s.Clear()
				default:
					backend, message, _, ok := s.Snapshot()
					if !ok {
						continue
					}
					if backend != secrets.BackendKeyring || message != failure.Error() {
						t.Errorf("torn snapshot: backend=%q message=%q", backend, message)
						return
					}
				}
			}
		}(g)
	}
	wg.Wait()

	s.Clear()
	if _, _, _, ok := s.Snapshot(); ok {
		t.Fatalf("state still active after a final Clear")
	}
}

// noPackageManager is a lookPath that finds nothing, which is what every wizard
// fixture below is pinned against: it makes the install row take its
// distro-neutral copy fallback, so the golden rows do not depend on what happens
// to be installed on the machine running the tests.
func noPackageManager(string) (string, error) { return "", errors.New("not found") }

// foundPackageManagers returns a lookPath that finds exactly the named binaries,
// so a test can stand a machine up as "an Arch box", "a Debian box", or one with
// several managers installed.
func foundPackageManagers(bins ...string) func(string) (string, error) {
	set := make(map[string]bool, len(bins))
	for _, b := range bins {
		set[b] = true
	}
	return func(name string) (string, error) {
		if set[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// keyringWizardRows is the full expected keyring wizard, first-time setup
// included, on a machine with no known package manager. The tests below slice
// it rather than rebuild it, so the two variants cannot drift apart.
func keyringWizardRows(message string) []providers.Result {
	return []providers.Result{
		{
			ID:       "totp:wizard:status",
			Title:    "OS keyring is not usable",
			Subtitle: message,
			Icon:     providers.Icon{ThemeName: "dialog-warning-symbolic"},
			Category: providers.CatTOTP,
			Score:    870,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}},
		},
		{
			ID:       "totp:wizard:keyring:daemon",
			Title:    "Start the Secret Service daemon",
			Subtitle: "Enter starts the daemon and retries setup",
			Icon:     providers.Icon{ThemeName: "media-playback-start-symbolic"},
			Category: providers.CatTOTP,
			Score:    860,
			Action:   providers.Action{Kind: ActTOTPWizardFix, Argv: []string{"keyring", "keyring:daemon"}},
		},
		{
			// Form is nil here because reflect.DeepEqual cannot compare Build
			// closures; the comparing test asserts the real Form separately
			// and nils it before the whole-slice comparison. The form's own
			// contract is pinned by TestWizardCreateKeyringForm.
			ID:       "totp:wizard:keyring:create",
			Title:    "Create or unlock the login keyring",
			Subtitle: "Enter asks for a keyring password, then retries setup",
			Icon:     providers.Icon{ThemeName: "dialog-password-symbolic"},
			Category: providers.CatTOTP,
			Score:    850,
		},
		{
			ID:       "totp:wizard:keyring:install",
			Title:    "Install a Secret Service provider",
			Subtitle: "Enter copies: gnome-keyring — install with your package manager",
			Icon:     providers.Icon{ThemeName: "edit-copy-symbolic"},
			Category: providers.CatTOTP,
			Score:    840,
			Action:   providers.Action{Kind: providers.ActClipboardCopy, Text: "gnome-keyring"},
		},
		{
			ID:       "totp:wizard:keyring:dbus",
			Title:    "Check the session bus",
			Subtitle: "Enter copies: echo $DBUS_SESSION_BUS_ADDRESS — empty means no session bus",
			Icon:     providers.Icon{ThemeName: "edit-copy-symbolic"},
			Category: providers.CatTOTP,
			Score:    830,
			Action:   providers.Action{Kind: providers.ActClipboardCopy, Text: "echo $DBUS_SESSION_BUS_ADDRESS"},
		},
		{
			ID:       "totp:wizard:retry",
			Title:    "Retry OS keyring setup",
			Subtitle: "Runs the keyring write/delete probe again",
			Icon:     providers.Icon{ThemeName: "view-refresh-symbolic"},
			Category: providers.CatTOTP,
			Score:    820,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}},
		},
		{
			ID:       "totp:wizard:back",
			Title:    "Choose a different backend",
			Subtitle: "Return to the backend choices",
			Icon:     providers.Icon{ThemeName: "go-previous-symbolic"},
			Category: providers.CatTOTP,
			Score:    810,
			Action:   providers.Action{Kind: ActTOTPWizardReset},
		},
	}
}

func TestWizardResultsKeyring(t *testing.T) {
	const message = "the OS keyring is not usable: no session bus"

	tests := []struct {
		name      string
		offerBack bool
		want      []providers.Result
	}{
		{
			name:      "first-time setup offers the escape hatch",
			offerBack: true,
			want:      keyringWizardRows(message),
		},
		{
			name:      "an established backend does not",
			offerBack: false,
			want:      keyringWizardRows(message)[:6],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardResults(secrets.BackendKeyring, message, tt.offerBack, noPackageManager)
			// The create row's Build closure defeats DeepEqual; prove the form
			// is there, then compare everything else against the fixture.
			for i := range got {
				if got[i].ID != "totp:wizard:keyring:create" {
					continue
				}
				if got[i].Form == nil {
					t.Fatal("create row has no Form")
				}
				got[i].Form = nil
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wizardResults mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

// TestWizardCreateKeyringForm pins the create-keyring form: its fields, the
// password-confirmation check, and that the built action carries the password
// under wizardPasswordKey and nothing else — the confirmation must not travel.
func TestWizardCreateKeyringForm(t *testing.T) {
	rows := wizardResults(secrets.BackendKeyring, "boom", true, noPackageManager)
	var form *providers.Form
	for _, r := range rows {
		if r.ID == "totp:wizard:keyring:create" {
			form = r.Form
		}
	}
	if form == nil {
		t.Fatal("no create row form")
	}
	if form.Title != "Create or unlock the login keyring" {
		t.Errorf("Form.Title = %q", form.Title)
	}
	wantFields := []providers.FormField{
		{Key: "password", Label: "Keyring password", Placeholder: "leave empty to store the keyring unencrypted", Secret: true},
		{Key: "confirm", Label: "Repeat password", Placeholder: "same password again", Secret: true},
	}
	if !reflect.DeepEqual(form.Fields, wantFields) {
		t.Errorf("Form.Fields = %+v, want %+v", form.Fields, wantFields)
	}

	tests := []struct {
		name     string
		values   map[string]string
		wantErr  bool
		wantVals map[string]string
	}{
		{
			name:     "matching passwords build the fix action",
			values:   map[string]string{"password": "hunter2", "confirm": "hunter2"},
			wantVals: map[string]string{"password": "hunter2"},
		},
		{
			name:     "empty on both sides is a legitimate blank password",
			values:   map[string]string{"password": "", "confirm": ""},
			wantVals: map[string]string{"password": ""},
		},
		{
			name:    "mismatched passwords are refused",
			values:  map[string]string{"password": "hunter2", "confirm": "hunter3"},
			wantErr: true,
		},
		{
			name:    "one side empty is a mismatch, not a blank password",
			values:  map[string]string{"password": "hunter2", "confirm": ""},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := form.Build(tt.values)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Build succeeded, want a mismatch error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Build error: %v", err)
			}
			want := providers.Action{
				Kind:   ActTOTPWizardFix,
				Argv:   []string{"keyring", "keyring:create"},
				Values: tt.wantVals,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Build() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestWizardResultsUnknownBackend pins the shape a backend with no guidance
// table gets — the slot a future remote backend drops into.
func TestWizardResultsUnknownBackend(t *testing.T) {
	tests := []struct {
		name      string
		offerBack bool
		wantIDs   []string
	}{
		{
			name:      "header and retry only",
			offerBack: false,
			wantIDs:   []string{"totp:wizard:status", "totp:wizard:retry"},
		},
		{
			name:      "plus the escape hatch during first-time setup",
			offerBack: true,
			wantIDs:   []string{"totp:wizard:status", "totp:wizard:retry", "totp:wizard:back"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardResults("nimbus", "not configured", tt.offerBack, noPackageManager)
			if !reflect.DeepEqual(ids(got), tt.wantIDs) {
				t.Fatalf("ids = %v, want %v", ids(got), tt.wantIDs)
			}
			if got[0].Title != "The nimbus backend is not usable" {
				t.Errorf("header title = %q", got[0].Title)
			}
			if got[0].Subtitle != "not configured" {
				t.Errorf("header subtitle = %q", got[0].Subtitle)
			}
			if got[1].Title != "Retry nimbus setup" {
				t.Errorf("retry title = %q", got[1].Title)
			}
		})
	}
}

// TestWizardResultsInvariants guards the two properties every wizard must hold
// whatever backend it describes: the rows arrive in a strictly descending score
// order (the aggregator sorts by -Score, so equal scores would let the Title
// tiebreak scramble diagnosis, fix and retry), and no row carries a zero
// Action, which the dispatcher answers with an "unknown action" error toast.
// The lookPath axis is here because the install row's kind depends on it: a
// machine with a package manager gets a terminal row and one without gets a copy
// row, and both must satisfy every invariant above. The guidance cap is asserted
// alongside them because it is a scoring invariant — a fourth row would score
// wizardRetryScore and collide with the retry row.
func TestWizardResultsInvariants(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		offerBack bool
		lookPath  func(string) (string, error)
	}{
		{name: "keyring, first time", backend: secrets.BackendKeyring, offerBack: true, lookPath: noPackageManager},
		{name: "keyring, established", backend: secrets.BackendKeyring, offerBack: false, lookPath: noPackageManager},
		{name: "keyring with a package manager, first time", backend: secrets.BackendKeyring, offerBack: true, lookPath: foundPackageManagers("apt")},
		{name: "keyring with a package manager, established", backend: secrets.BackendKeyring, offerBack: false, lookPath: foundPackageManagers("apt")},
		{name: "keyring with no lookPath wired up", backend: secrets.BackendKeyring, offerBack: true, lookPath: nil},
		{name: "unknown, first time", backend: "nimbus", offerBack: true, lookPath: noPackageManager},
		{name: "unknown, established", backend: "nimbus", offerBack: false, lookPath: noPackageManager},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if n := len(wizardGuidance(tt.backend, tt.lookPath)); n > 4 {
				t.Errorf("wizardGuidance(%q) returned %d rows, want at most 4", tt.backend, n)
			}
			got := wizardResults(tt.backend, "boom", tt.offerBack, tt.lookPath)
			if len(got) == 0 {
				t.Fatal("no wizard rows")
			}
			for i, r := range got {
				// A row must be activatable one way or the other: a dispatchable
				// Action, or a Form that replaces the primary activation.
				if r.Action.Kind == "" && r.Form == nil {
					t.Errorf("row %d (%s) has neither an Action nor a Form", i, r.ID)
				}
				if r.Category != providers.CatTOTP {
					t.Errorf("row %d (%s) category = %d, want CatTOTP", i, r.ID, r.Category)
				}
				if r.Icon.ThemeName == "" {
					t.Errorf("row %d (%s) has no icon", i, r.ID)
				}
				if i > 0 && r.Score >= got[i-1].Score {
					t.Errorf("row %d (%s) scores %d, not below row %d's %d", i, r.ID, r.Score, i-1, got[i-1].Score)
				}
			}
			if got[0].Score <= TriggerScore {
				t.Errorf("wizard header scores %d, not above TriggerScore %d", got[0].Score, TriggerScore)
			}
		})
	}
}

// TestWizardGuidanceInstallRow pins the install row against every package
// manager banshee probes for, including the exact terminal argv.
//
// The expectations are written out as literals rather than built with
// terminalWrap on purpose: this argv is a wire contract with the user's shell —
// `sh -c` word-splitting, the quoting of the hold-open prompt, the trailing
// `read` — and a test that called the code under test to compute its own
// expectation would happily ratify a broken rewrite of it.
func TestWizardGuidanceInstallRow(t *testing.T) {
	tests := []struct {
		name  string
		found []string
		want  guidanceRow
	}{
		{
			name:  "pacman",
			found: []string{"pacman"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceTerminal,
				title:    "Install a Secret Service provider",
				subtitle: "Enter opens a terminal to install it (sudo prompts there)",
				argv:     []string{"sh", "-c", `sudo pacman -S --needed gnome-keyring; printf '\nPress Enter to close\n'; read _`},
			},
		},
		{
			name:  "apt",
			found: []string{"apt"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceTerminal,
				title:    "Install a Secret Service provider",
				subtitle: "Enter opens a terminal to install it (sudo prompts there)",
				argv:     []string{"sh", "-c", `sudo apt install gnome-keyring; printf '\nPress Enter to close\n'; read _`},
			},
		},
		{
			name:  "dnf",
			found: []string{"dnf"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceTerminal,
				title:    "Install a Secret Service provider",
				subtitle: "Enter opens a terminal to install it (sudo prompts there)",
				argv:     []string{"sh", "-c", `sudo dnf install gnome-keyring; printf '\nPress Enter to close\n'; read _`},
			},
		},
		{
			name:  "zypper",
			found: []string{"zypper"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceTerminal,
				title:    "Install a Secret Service provider",
				subtitle: "Enter opens a terminal to install it (sudo prompts there)",
				argv:     []string{"sh", "-c", `sudo zypper install gnome-keyring; printf '\nPress Enter to close\n'; read _`},
			},
		},
		{
			// Probe order decides, not the caller's argument order: the table's
			// first entry wins on a machine carrying several managers.
			name:  "several present, the first probed wins",
			found: []string{"zypper", "dnf", "apt", "pacman"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceTerminal,
				title:    "Install a Secret Service provider",
				subtitle: "Enter opens a terminal to install it (sudo prompts there)",
				argv:     []string{"sh", "-c", `sudo pacman -S --needed gnome-keyring; printf '\nPress Enter to close\n'; read _`},
			},
		},
		{
			// The fallback, spelled out byte for byte: an unknown distro must
			// keep exactly the advice it had before banshee learned to probe.
			name:  "no known package manager falls back to the copy row",
			found: nil,
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceCopy,
				title:    "Install a Secret Service provider",
				subtitle: "Enter copies: gnome-keyring — install with your package manager",
				cmd:      "gnome-keyring",
			},
		},
		{
			name:  "an unrelated binary on PATH is not a package manager",
			found: []string{"gnome-keyring"},
			want: guidanceRow{
				id:       "keyring:install",
				kind:     guidanceCopy,
				title:    "Install a Secret Service provider",
				subtitle: "Enter copies: gnome-keyring — install with your package manager",
				cmd:      "gnome-keyring",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := installGuidance(foundPackageManagers(tt.found...))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("installGuidance mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
			// The row is only useful if it reaches the user as the Action its
			// kind promises, so check the one derivation the wizard performs.
			action := got.action(secrets.BackendKeyring)
			if tt.want.kind == guidanceTerminal {
				if action.Kind != providers.ActTerminal || !reflect.DeepEqual(action.Argv, tt.want.argv) {
					t.Errorf("action = %+v, want ActTerminal with %v", action, tt.want.argv)
				}
			} else if action.Kind != providers.ActClipboardCopy || action.Text != tt.want.cmd {
				t.Errorf("action = %+v, want a clipboard copy of %q", action, tt.want.cmd)
			}
		})
	}
}

// TestLookupWizardFix is the gate the fix handler stands on: only pairs this
// package published may ever be executed, so an id from another backend — or one
// nobody defined — must come back not-ok rather than borrowing someone's argv.
func TestLookupWizardFix(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		id      string
		wantOK  bool
		wantAr  []string
	}{
		{
			name:    "the keyring daemon fix",
			backend: secrets.BackendKeyring,
			id:      "keyring:daemon",
			wantOK:  true,
			wantAr:  []string{"systemctl", "--user", "start", "gnome-keyring-daemon"},
		},
		{
			name:    "an id the backend does not define",
			backend: secrets.BackendKeyring,
			id:      "keyring:install",
			wantOK:  false,
		},
		{
			name:    "a backend with no fixes at all",
			backend: secrets.BackendPlaintext,
			id:      "keyring:daemon",
			wantOK:  false,
		},
		{
			name:    "an unknown backend",
			backend: "nimbus",
			id:      "keyring:daemon",
			wantOK:  false,
		},
		{
			name:    "empty key halves",
			backend: "",
			id:      "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fix, ok := lookupWizardFix(tt.backend, tt.id)
			if ok != tt.wantOK {
				t.Fatalf("lookupWizardFix(%q, %q) ok = %v, want %v", tt.backend, tt.id, ok, tt.wantOK)
			}
			if !ok {
				if !reflect.DeepEqual(fix, wizardFix{}) {
					t.Errorf("a miss returned %+v, want the zero fix", fix)
				}
				return
			}
			if !reflect.DeepEqual(fix.argv, tt.wantAr) {
				t.Errorf("argv = %v, want %v", fix.argv, tt.wantAr)
			}
			if fix.failMsg == "" {
				t.Error("fix has no failMsg to head its error with")
			}
		})
	}
}

// wizardBackends is every backend name the reachability test holds the two
// wizard tables accountable for. It is spelled out rather than derived from
// wizardFixes because the dead-row half of the invariant is only checkable for a
// backend the fix table has never heard of: a guidanceRun row added under a
// backend with no wizardFixes entry is exactly the row whose Enter returns
// "no fix … for the … backend", and a loop driven by wizardFixes would never
// visit it. A new backend belongs here the moment secrets grows one.
var wizardBackends = []string{secrets.BackendKeyring, secrets.BackendPlaintext, secrets.BackendNimbus}

// TestWizardFixesAreReachable ties the two tables together: every entry in
// wizardFixes must be emitted by a guidanceRun row, and every guidanceRun row
// must resolve, or the wizard offers a row whose handler refuses it (or carries
// dead argv nobody can reach).
func TestWizardFixesAreReachable(t *testing.T) {
	backends := make(map[string]bool, len(wizardBackends))
	for _, backend := range wizardBackends {
		backends[backend] = true
	}
	// Union, so a fix table for a backend nobody listed above is still audited
	// rather than silently skipped.
	for backend := range wizardFixes {
		backends[backend] = true
	}

	for backend := range backends {
		emitted := make(map[string]bool)
		for _, g := range wizardGuidance(backend, noPackageManager) {
			if g.kind != guidanceRun {
				continue
			}
			emitted[g.id] = true
			if _, ok := lookupWizardFix(backend, g.id); !ok {
				t.Errorf("%s row %q is a run row with no entry in wizardFixes", backend, g.id)
			}
		}
		for id := range wizardFixes[backend] {
			if !emitted[id] {
				t.Errorf("wizardFixes[%s][%s] is not reachable from any guidance row", backend, id)
			}
		}
	}
}
