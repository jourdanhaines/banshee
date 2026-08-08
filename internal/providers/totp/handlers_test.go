package totp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/launch"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// asyncWait bounds how long a test waits for a detached handler goroutine. It
// is long enough that a loaded CI machine does not flake and short enough that
// a genuinely stuck handler fails the run rather than hanging it.
const asyncWait = 5 * time.Second

// testClock is an atomically settable clock, because the copy handler reads it
// from the goroutine it spawns while the test advances it from the main one.
type testClock struct{ unix atomic.Int64 }

func newTestClock(unix int64) *testClock {
	c := &testClock{}
	c.unix.Store(unix)
	return c
}

func (c *testClock) now() time.Time { return time.Unix(c.unix.Load(), 0) }

func (c *testClock) set(unix int64) { c.unix.Store(unix) }

// harness wires a dispatcher to fake collaborators and gives the test channels
// to synchronize on the detached paths.
type harness struct {
	disp      *launch.Dispatcher
	store     *fakeStore
	clock     *testClock
	indexPath string
	copied    chan string
	notified  chan string
}

// newHarness registers the handlers over idx, resolving every backend name to
// store.
func newHarness(t *testing.T, idx Index, store *fakeStore) *harness {
	t.Helper()
	h := &harness{
		disp:      launch.NewDispatcher(),
		store:     store,
		clock:     newTestClock(fixedUnix),
		indexPath: filepath.Join(t.TempDir(), "totp.json"),
		copied:    make(chan string, 4),
		notified:  make(chan string, 4),
	}
	if err := SaveIndex(h.indexPath, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	RegisterHandlersWith(h.disp, Deps{
		IndexPath: h.indexPath,
		OpenStore: func(name string) (secrets.Store, error) {
			if store == nil {
				return nil, errors.New("no store")
			}
			return store, nil
		},
		Copy:   func(text string) error { h.copied <- text; return nil },
		Now:    h.clock.now,
		Notify: func(msg string) { h.notified <- msg },
	})
	return h
}

func (h *harness) waitCopied(t *testing.T) string {
	t.Helper()
	select {
	case v := <-h.copied:
		return v
	case msg := <-h.notified:
		t.Fatalf("handler notified %q instead of copying", msg)
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for the clipboard write")
	}
	return ""
}

func (h *harness) waitNotified(t *testing.T) string {
	t.Helper()
	select {
	case msg := <-h.notified:
		return msg
	case v := <-h.copied:
		t.Fatalf("handler copied %q instead of reporting a failure", v)
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for a notification")
	}
	return ""
}

func (h *harness) index(t *testing.T) Index {
	t.Helper()
	idx, err := LoadIndex(h.indexPath)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	return idx
}

func TestCopyComputesCodeAtDispatchTime(t *testing.T) {
	h := newHarness(t, twoEntryIndex(), stockedStore())

	// The row the user looked at was rendered at fixedUnix; by the time they
	// press Enter the period has rolled over. The handler must copy the code
	// that is valid now, not the one that was on screen.
	action := providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}
	h.clock.set(fixedUnix + 30)

	if err := h.disp.Dispatch(action); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := h.waitCopied(t); got != codeNextStep {
		t.Errorf("copied %q, want the freshly rotated %q", got, codeNextStep)
	}
}

func TestCopyCopiesUngroupedCode(t *testing.T) {
	h := newHarness(t, twoEntryIndex(), stockedStore())
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := h.waitCopied(t); got != codeAtFixed {
		t.Errorf("copied %q, want %q with no grouping spaces", got, codeAtFixed)
	}
}

func TestCopyPassesCredentialToStore(t *testing.T) {
	store := stockedStore()
	store.auth = true
	h := newHarness(t, twoEntryIndex(), store)
	action := providers.Action{
		Kind:   ActTOTPCopy,
		Argv:   []string{"github"},
		Values: map[string]string{"credential": "hunter2"},
	}
	if err := h.disp.Dispatch(action); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	h.waitCopied(t)
	if got := store.lastCred().Password; got != "hunter2" {
		t.Errorf("store saw credential %q, want the submitted password", got)
	}
}

func TestCopyNotifiesOnAsyncFailure(t *testing.T) {
	tests := []struct {
		name     string
		getErr   error
		stored   string
		copyErr  error
		wantWord string
	}{
		{name: "backend refuses", getErr: errors.New("boom"), wantWord: "could not read"},
		{name: "backend wants a password", getErr: secrets.ErrAuthRequired, wantWord: "password"},
		{name: "stored seed is corrupt", stored: "!!!!", wantWord: "base32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := stockedStore()
			store.getErr = tt.getErr
			if tt.stored != "" {
				store.values["totp/github"] = tt.stored
			}
			h := newHarness(t, twoEntryIndex(), store)
			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}); err != nil {
				t.Fatalf("Dispatch returned %v, want the failure to arrive asynchronously", err)
			}
			if msg := h.waitNotified(t); !strings.Contains(msg, tt.wantWord) {
				t.Errorf("notification %q does not mention %q", msg, tt.wantWord)
			}
		})
	}
}

func TestCopySynchronousRejections(t *testing.T) {
	tests := []struct {
		name  string
		idx   Index
		argv  []string
		wantS string
	}{
		{name: "no name", idx: twoEntryIndex(), argv: nil, wantS: "no entry name"},
		{name: "blank name", idx: twoEntryIndex(), argv: []string{"  "}, wantS: "no entry name"},
		{name: "unknown entry", idx: twoEntryIndex(), argv: []string{"nope"}, wantS: "no entry named"},
		{
			name:  "no backend chosen",
			idx:   Index{V: IndexVersion, Entries: []Entry{{Name: "github"}}},
			argv:  []string{"github"},
			wantS: "no secrets backend",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.idx, stockedStore())
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: tt.argv})
			if err == nil {
				t.Fatalf("Dispatch succeeded, want an error mentioning %q", tt.wantS)
			}
			if !strings.Contains(err.Error(), tt.wantS) {
				t.Errorf("error %q does not mention %q", err, tt.wantS)
			}
		})
	}
}

func TestAddPersistsSecretThenMetadata(t *testing.T) {
	tests := []struct {
		name      string
		values    map[string]string
		wantName  string
		wantSeed  string
		wantEntry Entry
	}{
		{
			name:     "raw base32 seed",
			values:   map[string]string{"name": "github", "secret": "jbsw y3dp ehpk 3pxp"},
			wantName: "github",
			wantSeed: testSeed,
			wantEntry: Entry{
				Name: "github", Digits: 6, Period: 30, Algorithm: AlgSHA1,
				Created: time.Unix(fixedUnix, 0).UTC(),
			},
		},
		{
			name: "otpauth URI carries parameters",
			values: map[string]string{
				"name":   "work",
				"secret": "otpauth://totp/Example:alice@example.com?secret=" + testSeed + "&issuer=Example&digits=8&period=60&algorithm=SHA256",
			},
			wantName: "work",
			wantSeed: testSeed,
			wantEntry: Entry{
				Name: "work", Issuer: "Example", Digits: 8, Period: 60, Algorithm: AlgSHA256,
				Created: time.Unix(fixedUnix, 0).UTC(),
			},
		},
		{
			name: "blank name falls back to the URI account",
			values: map[string]string{
				"secret": "otpauth://totp/Example:alice@example.com?secret=" + testSeed + "&issuer=Example",
			},
			wantName: "alice@example.com",
			wantSeed: testSeed,
			wantEntry: Entry{
				Name: "alice@example.com", Issuer: "Example", Digits: 6, Period: 30, Algorithm: AlgSHA1,
				Created: time.Unix(fixedUnix, 0).UTC(),
			},
		},
		{
			name: "blank name and no account falls back to the issuer",
			values: map[string]string{
				"secret": "otpauth://totp/?secret=" + testSeed + "&issuer=Example",
			},
			wantName: "Example",
			wantSeed: testSeed,
			wantEntry: Entry{
				Name: "Example", Issuer: "Example", Digits: 6, Period: 30, Algorithm: AlgSHA1,
				Created: time.Unix(fixedUnix, 0).UTC(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("plaintext")
			h := newHarness(t, Index{V: IndexVersion, Backend: "plaintext"}, store)
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: tt.values})
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			seed, ok := store.get("totp/" + tt.wantName)
			if !ok || seed != tt.wantSeed {
				t.Fatalf("stored seed for %q = %q (present %v), want %q", tt.wantName, seed, ok, tt.wantSeed)
			}
			idx := h.index(t)
			got, found := idx.Find(tt.wantName)
			if !found {
				t.Fatalf("index has no entry %q, entries = %+v", tt.wantName, idx.Entries)
			}
			if got != tt.wantEntry {
				t.Errorf("entry = %+v, want %+v", got, tt.wantEntry)
			}
		})
	}
}

func TestAddSynchronousRejections(t *testing.T) {
	tests := []struct {
		name   string
		idx    Index
		setErr error
		values map[string]string
		wantS  string
	}{
		{
			name:   "no secret",
			idx:    Index{V: IndexVersion, Backend: "plaintext"},
			values: map[string]string{"name": "github"},
			wantS:  "a secret is required",
		},
		{
			name:   "not base32",
			idx:    Index{V: IndexVersion, Backend: "plaintext"},
			values: map[string]string{"name": "github", "secret": "!!!!"},
			wantS:  "base32",
		},
		{
			name:   "counter-based URI",
			idx:    Index{V: IndexVersion, Backend: "plaintext"},
			values: map[string]string{"name": "github", "secret": "otpauth://hotp/x?secret=" + testSeed},
			wantS:  "hotp",
		},
		{
			name:   "duplicate name",
			idx:    twoEntryIndex(),
			values: map[string]string{"name": "GitHub", "secret": testSeed},
			wantS:  "already exists",
		},
		{
			name:   "no backend chosen",
			idx:    Index{V: IndexVersion},
			values: map[string]string{"name": "github", "secret": testSeed},
			wantS:  "no secrets backend",
		},
		{
			name:   "unnamed raw seed",
			idx:    Index{V: IndexVersion, Backend: "plaintext"},
			values: map[string]string{"secret": testSeed},
			wantS:  "a name is required",
		},
		{
			name:   "backend refuses the write",
			idx:    Index{V: IndexVersion, Backend: "plaintext"},
			setErr: errors.New("vault is read-only"),
			values: map[string]string{"name": "github", "secret": testSeed},
			wantS:  "read-only",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("plaintext")
			store.setErr = tt.setErr
			h := newHarness(t, tt.idx, store)
			before := len(h.index(t).Entries)
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: tt.values})
			if err == nil {
				t.Fatalf("Dispatch succeeded, want an error mentioning %q", tt.wantS)
			}
			if !strings.Contains(err.Error(), tt.wantS) {
				t.Errorf("error %q does not mention %q", err, tt.wantS)
			}
			// A rejected add must leave no metadata behind, whether it was
			// refused before the write or by the backend itself.
			if after := len(h.index(t).Entries); after != before {
				t.Errorf("index entries = %d, want the original %d after a failed add", after, before)
			}
		})
	}
}

// TestAddDetachesForBlockingBackend pins the rule that keeps the daemon
// responsive: a backend that can wait on something outside this process — a
// keyring unlock prompt, a network round trip — is never written from the GTK
// main loop, so Dispatch returns immediately and the outcome arrives by
// notification.
func TestAddDetachesForBlockingBackend(t *testing.T) {
	store := newFakeStore("nimbus")
	store.auth = true
	store.blocking = true
	h := newHarness(t, Index{V: IndexVersion, Backend: "nimbus"}, store)
	values := map[string]string{"name": "github", "secret": testSeed, "credential": "hunter2"}
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if msg := h.waitNotified(t); !strings.Contains(msg, "github") {
		t.Errorf("notification %q does not name the entry", msg)
	}
	if got := store.lastCred().Password; got != "hunter2" {
		t.Errorf("store saw credential %q, want the submitted password", got)
	}
	if _, found := h.index(t).Find("github"); !found {
		t.Error("index has no entry after a successful detached add")
	}
}

// TestAddDetachesForBlockingLocalBackend is the OS keyring specifically: it
// does not authenticate per access, so keying the detach on AuthPerAccess would
// run its Secret Service write on the GTK main loop and freeze the daemon
// behind an unlock prompt.
func TestAddDetachesForBlockingLocalBackend(t *testing.T) {
	store := newFakeStore("keyring")
	store.blocking = true
	if store.AuthPerAccess() {
		t.Fatal("the fixture must not authenticate per access; that is the point of this test")
	}
	h := newHarness(t, Index{V: IndexVersion, Backend: "keyring"}, store)
	values := map[string]string{"name": "github", "secret": testSeed}
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if msg := h.waitNotified(t); !strings.Contains(msg, "github") {
		t.Errorf("notification %q does not name the entry", msg)
	}
	if _, found := h.index(t).Find("github"); !found {
		t.Error("index has no entry after a successful detached add")
	}
}

func TestSetupPersistsBackend(t *testing.T) {
	tests := []struct {
		name        string
		backend     string
		setErr      error
		delErr      error
		wantBackend string
		wantErr     string
		wantProbed  bool
	}{
		{name: "plaintext is not probed", backend: "plaintext", wantBackend: "plaintext"},
		{name: "keyring probe succeeds", backend: "keyring", wantBackend: "keyring", wantProbed: true},
		{
			name:    "keyring probe write fails",
			backend: "keyring",
			setErr:  errors.New("collection is locked"),
			wantErr: "not usable",
		},
		{
			name:       "keyring probe delete fails",
			backend:    "keyring",
			delErr:     errors.New("no such item"),
			wantErr:    "would not remove it",
			wantProbed: false,
		},
		{name: "nimbus is refused", backend: "nimbus", wantErr: "not available yet"},
		{name: "no backend name", backend: "", wantErr: "no backend name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore(tt.backend)
			store.setErr = tt.setErr
			store.delErr = tt.delErr
			h := newHarness(t, Index{V: IndexVersion}, store)

			var argv []string
			if tt.backend != "" {
				argv = []string{tt.backend}
			}
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: argv})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want one mentioning %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got := h.index(t).Backend; got != tt.wantBackend {
				t.Errorf("persisted backend = %q, want %q", got, tt.wantBackend)
			}
			// A completed probe cleans up after itself; a probe whose delete
			// failed is allowed to leave the marker behind — that is exactly
			// the broken backend the probe exists to catch.
			if _, leftover := store.get(probeKey); leftover && tt.delErr == nil {
				t.Error("probe secret was left in the store")
			}
			if tt.wantProbed && len(store.deletedKeys()) == 0 {
				t.Error("keyring was persisted without a completed probe")
			}
		})
	}
}

// TestSetupDetachesForBlockingBackend covers the keyring probe, which is the
// likeliest moment in the whole feature for a locked collection to raise an
// unlock prompt — it is the first thing banshee ever asks of the keyring. It
// must not run on the GTK main loop, so Dispatch returns immediately and both
// outcomes arrive by notification.
func TestSetupDetachesForBlockingBackend(t *testing.T) {
	tests := []struct {
		name        string
		setErr      error
		wantBackend string
		wantSub     string
	}{
		{name: "probe succeeds", wantBackend: "keyring", wantSub: "keyring"},
		{name: "probe fails", setErr: errors.New("collection is locked"), wantSub: "not usable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("keyring")
			store.blocking = true
			store.setErr = tt.setErr
			h := newHarness(t, Index{V: IndexVersion}, store)

			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
				t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
			}
			if msg := h.waitNotified(t); !strings.Contains(msg, tt.wantSub) {
				t.Errorf("notification %q does not mention %q", msg, tt.wantSub)
			}
			if got := h.index(t).Backend; got != tt.wantBackend {
				t.Errorf("persisted backend = %q, want %q", got, tt.wantBackend)
			}
		})
	}
}

// wizardStaleBackend is the backend named by the failure the wizard tests
// record before dispatching. A test that expects "the state was not touched"
// asserts this name survives, which is stronger than asserting nothing is
// recorded — an empty state would also pass if the handler wrongly cleared it.
const wizardStaleBackend = "earlier"

// wizardHarness is a harness whose handlers have the wizard wired: a shared
// SetupState to record failures in, and a Reopen that reports the query the
// launcher would have been reopened with instead of touching a UI.
//
// It is separate from newHarness rather than an option on it, because the whole
// point of the unwired harness is to prove the pre-wizard toast behavior still
// holds for a front-end that never supplies either collaborator.
type wizardHarness struct {
	*harness
	state    *SetupState
	reopened chan string
}

// newWizardHarness registers the handlers over idx with the wizard collaborators
// attached, resolving every backend name to store.
func newWizardHarness(t *testing.T, idx Index, store *fakeStore) *wizardHarness {
	t.Helper()
	h := &wizardHarness{
		harness: &harness{
			disp:      launch.NewDispatcher(),
			store:     store,
			clock:     newTestClock(fixedUnix),
			indexPath: filepath.Join(t.TempDir(), "totp.json"),
			copied:    make(chan string, 4),
			notified:  make(chan string, 4),
		},
		state:    &SetupState{},
		reopened: make(chan string, 4),
	}
	if err := SaveIndex(h.indexPath, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	RegisterHandlersWith(h.disp, Deps{
		IndexPath: h.indexPath,
		OpenStore: func(name string) (secrets.Store, error) {
			if store == nil {
				return nil, errors.New("no store")
			}
			return store, nil
		},
		Copy:   func(text string) error { h.copied <- text; return nil },
		Now:    h.clock.now,
		Notify: func(msg string) { h.notified <- msg },
		State:  h.state,
		Reopen: func(q string) { h.reopened <- q },
	})
	return h
}

// waitReopened blocks until the handlers reopen the launcher and returns the
// query they asked for. It fails on a toast or a clipboard write instead,
// because with the wizard wired those are the wrong channel — the error belongs
// on screen as rows.
func (h *wizardHarness) waitReopened(t *testing.T) string {
	t.Helper()
	select {
	case q := <-h.reopened:
		return q
	case msg := <-h.notified:
		t.Fatalf("handler notified %q instead of reopening the launcher", msg)
	case v := <-h.copied:
		t.Fatalf("handler copied %q instead of reopening the launcher", v)
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for the launcher to be reopened")
	}
	return ""
}

// assertQuiet fails if a toast is queued. Every handler path returns right after
// it reopens or notifies, so once the awaited event has arrived an empty
// notification channel proves the other channel was never used.
func (h *wizardHarness) assertQuiet(t *testing.T) {
	t.Helper()
	select {
	case msg := <-h.notified:
		t.Fatalf("handler also notified %q; the wizard rows are the whole report", msg)
	default:
	}
}

// assertNotReopened fails if the launcher was reopened, which is how the tests
// pin "this failure is a toast, never the wizard".
func (h *wizardHarness) assertNotReopened(t *testing.T) {
	t.Helper()
	select {
	case q := <-h.reopened:
		t.Fatalf("handler reopened the launcher on %q; this failure is not the backend's fault", q)
	default:
	}
}

// blockIndexWrites makes every later SaveIndex on path fail while LoadIndex
// keeps working, which is the "the disk said no" case the wizard must stay out
// of. It plants a directory where SaveIndex's temporary file has to go rather
// than making the parent read-only, because file modes do not stop root and this
// suite must behave the same in a container.
func blockIndexWrites(t *testing.T, path string) {
	t.Helper()
	blocker := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatalf("planting the index write blocker: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(blocker) })
}

// TestSetupFailureOpensWizard is the headline behavior: a keyring that refuses
// the probe puts its own error on screen as wizard rows and says nothing to the
// notification daemon.
func TestSetupFailureOpensWizard(t *testing.T) {
	store := newFakeStore("keyring")
	store.blocking = true
	store.setErr = errors.New("collection is locked")
	h := newWizardHarness(t, Index{V: IndexVersion}, store)

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger so the wizard rows render", q)
	}
	h.assertQuiet(t)

	backend, msg, ok := h.state.Snapshot()
	if !ok {
		t.Fatal("no failure recorded, so the next query would render the setup rows again")
	}
	if backend != "keyring" {
		t.Errorf("recorded backend = %q, want %q", backend, "keyring")
	}
	if !strings.Contains(msg, "not usable") {
		t.Errorf("recorded message %q does not carry the probe's own diagnosis", msg)
	}
	if got := h.index(t).Backend; got != "" {
		t.Errorf("persisted backend = %q, want the index untouched by a failed probe", got)
	}
}

// TestSetupSuccessClearsAndReopens covers the retry that works: the choice is
// persisted, the recorded failure is dropped and the user is put back in the
// launcher, which is both the confirmation and the next step.
func TestSetupSuccessClearsAndReopens(t *testing.T) {
	store := newFakeStore("keyring")
	store.blocking = true
	h := newWizardHarness(t, Index{V: IndexVersion}, store)
	h.state.Fail(wizardStaleBackend, errors.New("stale failure"))

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	h.assertQuiet(t)
	if _, _, ok := h.state.Snapshot(); ok {
		t.Error("a completed setup left a failure recorded, so the wizard would still be on screen")
	}
	if got := h.index(t).Backend; got != "keyring" {
		t.Errorf("persisted backend = %q, want %q", got, "keyring")
	}
}

// TestSetupClearsStateWhenPersistFails pins where the clear belongs: a probe
// that passed has proved the backend, so the wizard must go even when the index
// write that follows does not. Leaving it recorded would put a disproved
// diagnosis back on screen on the next trigger, with no way out but a later
// successful copy.
func TestSetupClearsStateWhenPersistFails(t *testing.T) {
	store := newFakeStore("keyring")
	store.blocking = true
	h := newWizardHarness(t, Index{V: IndexVersion}, store)
	h.state.Fail("keyring", errors.New("the OS keyring is not usable: collection is locked"))
	blockIndexWrites(t, h.indexPath)

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
	}
	if msg := h.waitNotified(t); msg == "" {
		t.Error("the index failure was reported nowhere")
	}
	h.assertNotReopened(t)
	if backend, msg, ok := h.state.Snapshot(); ok {
		t.Errorf("recorded failure = (%q, %q) after a successful probe, want it cleared", backend, msg)
	}
}

// TestSetupRetryRetiresWizardForNonBlockingBackend covers the wizard's own
// escape for a backend that never detaches: a plaintext store that failed a read
// raises the same wizard, and its "Retry" row runs the synchronous setup path.
// That path must retire the wizard and put the user back, or the row it offers
// does nothing at all.
func TestSetupRetryRetiresWizardForNonBlockingBackend(t *testing.T) {
	store := newFakeStore("plaintext")
	if store.Blocking() {
		t.Fatal("the fixture must not block; this test is the synchronous setup path")
	}
	h := newWizardHarness(t, Index{V: IndexVersion, Backend: "plaintext"}, store)
	h.state.Fail("plaintext", errors.New("plaintext: parse totp.json: unexpected end of JSON input"))

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"plaintext"}}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, _, ok := h.state.Snapshot(); ok {
		t.Error("retry left the failure recorded, so the same wizard would come straight back")
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	if got := h.index(t).Backend; got != "plaintext" {
		t.Errorf("persisted backend = %q, want %q", got, "plaintext")
	}
}

// TestAddRoutesUnusableBackendForNonBlockingBackend is the add half of the same
// gap: the documented rule is that a backend-unusable failure opens the wizard,
// and it has to hold for the synchronous write path too, not only the detached
// one.
func TestAddRoutesUnusableBackendForNonBlockingBackend(t *testing.T) {
	store := newFakeStore("plaintext")
	store.setErr = errors.New("vault is read-only")
	h := newWizardHarness(t, Index{V: IndexVersion, Backend: "plaintext"}, store)

	values := map[string]string{"name": "github", "secret": testSeed}
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
		t.Fatalf("Dispatch returned %v, want the failure reported as wizard rows", err)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	h.assertQuiet(t)
	backend, msg, ok := h.state.Snapshot()
	if !ok || backend != "plaintext" {
		t.Fatalf("recorded failure = (%q, %v), want the index's backend", backend, ok)
	}
	if !strings.Contains(msg, "github") || strings.Contains(msg, testSeed) {
		t.Errorf("recorded message %q must name the entry and never the seed", msg)
	}
	if len(h.index(t).Entries) != 0 {
		t.Error("a refused write left metadata behind")
	}
}

// TestUnusableFailureFallsBackToToast covers the two ways the wizard cannot
// happen even though a backend failed. Reopening anyway would steal focus onto
// rows that do not mention the failure, leaving it reported nowhere.
func TestUnusableFailureFallsBackToToast(t *testing.T) {
	tests := []struct {
		name string
		// idx is the index as it stands when the detached probe finally fails.
		idx Index
		// wireState wires the failure record; false is the front-end the Deps
		// godoc describes, which keeps the toast behavior.
		wireState bool
	}{
		{
			// The user grew tired of waiting on the wedged keyring, reopened the
			// launcher and picked the plaintext backend, which persisted while the
			// probe was still running.
			name:      "the index already names another backend",
			idx:       Index{V: IndexVersion, Backend: "plaintext"},
			wireState: true,
		},
		{
			name: "no failure record is wired",
			idx:  Index{V: IndexVersion},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("keyring")
			store.blocking = true
			store.setErr = errors.New("collection is locked")
			h := newWizardHarness(t, tt.idx, store)
			if !tt.wireState {
				// Register replaces by kind, so this is the same harness with one
				// collaborator taken away.
				RegisterHandlersWith(h.disp, Deps{
					IndexPath: h.indexPath,
					OpenStore: func(string) (secrets.Store, error) { return store, nil },
					Now:       h.clock.now,
					Notify:    func(msg string) { h.notified <- msg },
					Reopen:    func(q string) { h.reopened <- q },
				})
			}

			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
				t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
			}
			if msg := h.waitNotified(t); !strings.Contains(msg, "not usable") {
				t.Errorf("notification %q does not carry the probe's diagnosis", msg)
			}
			h.assertNotReopened(t)
			if _, _, ok := h.state.Snapshot(); ok {
				t.Error("a failure the provider would discard was recorded anyway")
			}
		})
	}
}

// TestCopyRoutingMatrix pins the classification rule at the one call site that
// reads a secret: only a backend that is actually broken opens the wizard.
func TestCopyRoutingMatrix(t *testing.T) {
	tests := []struct {
		name       string
		getErr     error
		wantWizard bool
		wantWord   string
	}{
		{name: "backend refuses", getErr: errors.New("boom"), wantWizard: true, wantWord: "could not read"},
		{name: "backend not configured", getErr: secrets.ErrNotConfigured, wantWizard: true, wantWord: "could not read"},
		// A Secret Service that never answers is the wedged daemon the wizard
		// exists for, even though banshee is the one that gave up waiting.
		{name: "backend never answered", getErr: context.DeadlineExceeded, wantWizard: true, wantWord: "could not read"},
		{name: "backend wants a password", getErr: secrets.ErrAuthRequired, wantWord: "password"},
		{name: "key is absent", getErr: secrets.ErrNotFound, wantWord: "could not read"},
		{name: "banshee cancelled the read", getErr: context.Canceled, wantWord: "could not read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := stockedStore()
			store.getErr = tt.getErr
			h := newWizardHarness(t, twoEntryIndex(), store)
			h.state.Fail(wizardStaleBackend, errors.New("stale failure"))

			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}); err != nil {
				t.Fatalf("Dispatch returned %v, want the failure to arrive asynchronously", err)
			}
			if !tt.wantWizard {
				if msg := h.waitNotified(t); !strings.Contains(msg, tt.wantWord) {
					t.Errorf("notification %q does not mention %q", msg, tt.wantWord)
				}
				h.assertNotReopened(t)
				if backend, _, ok := h.state.Snapshot(); !ok || backend != wizardStaleBackend {
					t.Errorf("recorded failure = (%q, %v), want the earlier one left alone", backend, ok)
				}
				return
			}
			if q := h.waitReopened(t); q != "totp" {
				t.Errorf("reopened on %q, want the provider's trigger", q)
			}
			h.assertQuiet(t)
			backend, msg, ok := h.state.Snapshot()
			if !ok || backend != "plaintext" {
				t.Fatalf("recorded failure = (%q, %v), want the index's backend", backend, ok)
			}
			if !strings.Contains(msg, tt.wantWord) || !strings.Contains(msg, "github") {
				t.Errorf("recorded message %q does not say which read failed", msg)
			}
		})
	}
}

// TestAddRoutingMatrix pins the same rule at the write call site, and the
// boundary beside it: the index write is banshee's own disk, never the vault.
func TestAddRoutingMatrix(t *testing.T) {
	tests := []struct {
		name             string
		setErr           error
		blockIndex       bool
		wantWizard       bool
		wantStateBackend string
	}{
		{
			name:             "backend refuses the write",
			setErr:           errors.New("vault is read-only"),
			wantWizard:       true,
			wantStateBackend: "keyring",
		},
		{
			name:             "backend wants a password",
			setErr:           secrets.ErrAuthRequired,
			wantStateBackend: wizardStaleBackend,
		},
		{
			// The seed is already stored, so the backend has proved itself and
			// the earlier failure is dropped; the index failure is still only a
			// toast.
			name:             "index write fails after the seed lands",
			blockIndex:       true,
			wantStateBackend: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("keyring")
			store.blocking = true
			store.setErr = tt.setErr
			h := newWizardHarness(t, Index{V: IndexVersion, Backend: "keyring"}, store)
			h.state.Fail(wizardStaleBackend, errors.New("stale failure"))
			if tt.blockIndex {
				blockIndexWrites(t, h.indexPath)
			}

			values := map[string]string{"name": "github", "secret": testSeed}
			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
				t.Fatalf("Dispatch returned %v, want the write to run detached", err)
			}
			if tt.wantWizard {
				if q := h.waitReopened(t); q != "totp" {
					t.Errorf("reopened on %q, want the provider's trigger", q)
				}
				h.assertQuiet(t)
			} else {
				if msg := h.waitNotified(t); !strings.Contains(msg, "could not add") {
					t.Errorf("notification %q does not report the failed add", msg)
				}
				h.assertNotReopened(t)
			}
			backend, msg, ok := h.state.Snapshot()
			if backend != tt.wantStateBackend {
				t.Errorf("recorded backend = %q (%v), want %q", backend, ok, tt.wantStateBackend)
			}
			if tt.wantWizard && !strings.Contains(msg, "github") {
				t.Errorf("recorded message %q does not name the entry that failed", msg)
			}
			if strings.Contains(msg, testSeed) {
				t.Error("the recorded message carries the seed; setup state must never hold secret material")
			}
		})
	}
}

// TestSuccessfulOpsClearState is the other half of the wizard's lifecycle: it
// disappears on its own the moment the backend does something successfully, so
// a user who fixed their keyring outside banshee never has to dismiss it.
func TestSuccessfulOpsClearState(t *testing.T) {
	tests := []struct {
		name     string
		dispatch providers.Action
	}{
		{
			name:     "a successful copy",
			dispatch: providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}},
		},
		{
			name:     "a successful add",
			dispatch: providers.Action{Kind: ActTOTPAdd, Values: map[string]string{"name": "work", "secret": testSeed}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newWizardHarness(t, twoEntryIndex(), stockedStore())
			h.state.Fail(wizardStaleBackend, errors.New("stale failure"))

			if err := h.disp.Dispatch(tt.dispatch); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if tt.dispatch.Kind == ActTOTPCopy {
				h.waitCopied(t)
			}
			if _, _, ok := h.state.Snapshot(); ok {
				t.Error("a successful backend operation left the failure recorded, so the wizard would linger")
			}
			h.assertNotReopened(t)
		})
	}
}

// TestWizardResetHandler covers the escape hatch, including the build where
// nobody wired the wizard up: the handler must never fail, because its only job
// is to put the user back where they were.
func TestWizardResetHandler(t *testing.T) {
	t.Run("wired", func(t *testing.T) {
		h := newWizardHarness(t, Index{V: IndexVersion}, newFakeStore("keyring"))
		h.state.Fail("keyring", errors.New("the OS keyring is not usable"))

		if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPWizardReset}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if _, _, ok := h.state.Snapshot(); ok {
			t.Error("the reset left the failure recorded, so the chooser would not come back")
		}
		if q := h.waitReopened(t); q != "totp" {
			t.Errorf("reopened on %q, want the provider's trigger", q)
		}
	})
	t.Run("unwired", func(t *testing.T) {
		h := newHarness(t, Index{V: IndexVersion}, newFakeStore("keyring"))
		if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPWizardReset}); err != nil {
			t.Fatalf("Dispatch with no State or Reopen: %v", err)
		}
		select {
		case msg := <-h.notified:
			t.Fatalf("unwired reset notified %q, want it to do nothing at all", msg)
		default:
		}
	})
}

func TestDispatchRejectsUnregisteredKind(t *testing.T) {
	h := newHarness(t, twoEntryIndex(), stockedStore())
	if err := h.disp.Dispatch(providers.Action{Kind: "totp-delete"}); err == nil {
		t.Error("Dispatch of an unregistered kind succeeded, want an error")
	}
}
