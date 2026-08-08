package totp

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

func TestSetupStateLifecycle(t *testing.T) {
	tests := []struct {
		name string
		// state builds the state under test and applies the operations; a nil
		// return exercises the nil-receiver path every method must survive.
		state       func() *SetupState
		wantBackend string
		wantMessage string
		wantOK      bool
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
				s.Clear()
				return s
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, message, ok := tt.state().Snapshot()
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if backend != tt.wantBackend {
				t.Errorf("backend = %q, want %q", backend, tt.wantBackend)
			}
			if message != tt.wantMessage {
				t.Errorf("message = %q, want %q", message, tt.wantMessage)
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
					s.Fail(secrets.BackendKeyring, failure)
				case 1:
					s.Clear()
				default:
					backend, message, ok := s.Snapshot()
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
	if _, _, ok := s.Snapshot(); ok {
		t.Fatalf("state still active after a final Clear")
	}
}

// keyringWizardRows is the full expected keyring wizard, first-time setup
// included. The tests below slice it rather than rebuild it, so the two
// variants cannot drift apart.
func keyringWizardRows(message string) []providers.Result {
	return []providers.Result{
		{
			ID:       "totp:wizard:status",
			Title:    "OS keyring is not usable",
			Subtitle: message,
			Icon:     providers.Icon{ThemeName: "dialog-warning-symbolic"},
			Category: providers.CatTOTP,
			Score:    860,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}},
		},
		{
			ID:       "totp:wizard:keyring:daemon",
			Title:    "Start the Secret Service daemon",
			Subtitle: "Enter copies: systemctl --user start gnome-keyring-daemon",
			Icon:     providers.Icon{ThemeName: "edit-copy-symbolic"},
			Category: providers.CatTOTP,
			Score:    850,
			Action:   providers.Action{Kind: providers.ActClipboardCopy, Text: "systemctl --user start gnome-keyring-daemon"},
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
		firstTime bool
		want      []providers.Result
	}{
		{
			name:      "first-time setup offers the escape hatch",
			firstTime: true,
			want:      keyringWizardRows(message),
		},
		{
			name:      "an established backend does not",
			firstTime: false,
			want:      keyringWizardRows(message)[:5],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardResults(secrets.BackendKeyring, message, tt.firstTime)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("wizardResults mismatch\n got: %+v\nwant: %+v", got, tt.want)
			}
		})
	}
}

// TestWizardResultsUnknownBackend pins the shape a backend with no guidance
// table gets — the slot a future remote backend drops into.
func TestWizardResultsUnknownBackend(t *testing.T) {
	tests := []struct {
		name      string
		firstTime bool
		wantIDs   []string
	}{
		{
			name:      "header and retry only",
			firstTime: false,
			wantIDs:   []string{"totp:wizard:status", "totp:wizard:retry"},
		},
		{
			name:      "plus the escape hatch during first-time setup",
			firstTime: true,
			wantIDs:   []string{"totp:wizard:status", "totp:wizard:retry", "totp:wizard:back"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardResults("nimbus", "not configured", tt.firstTime)
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
func TestWizardResultsInvariants(t *testing.T) {
	tests := []struct {
		name      string
		backend   string
		firstTime bool
	}{
		{name: "keyring, first time", backend: secrets.BackendKeyring, firstTime: true},
		{name: "keyring, established", backend: secrets.BackendKeyring, firstTime: false},
		{name: "unknown, first time", backend: "nimbus", firstTime: true},
		{name: "unknown, established", backend: "nimbus", firstTime: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wizardResults(tt.backend, "boom", tt.firstTime)
			if len(got) == 0 {
				t.Fatal("no wizard rows")
			}
			for i, r := range got {
				if r.Action.Kind == "" {
					t.Errorf("row %d (%s) has an empty Action.Kind", i, r.ID)
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
