package totp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	// ran records every argv the fix handler asked Deps.Run to execute, which is
	// how this suite proves what banshee would have run without running it.
	ran chan []string
	// runOut and runErr are what the injected Run answers with. They are plain
	// fields rather than channels because a test sets them before it dispatches
	// and never touches them again — the `go` statement inside the handler
	// orders that write ahead of the goroutine's read.
	runOut []byte
	runErr error
	// ranInput records every RunInput call the fix handler makes: the argv and
	// exactly what was piped to the command's stdin, so a test can prove the
	// password went to stdin and nowhere else. Answers with runInputOut and
	// runInputErr, which are separate from Run's so a test can fail the two
	// steps of a chained fix independently.
	ranInput    chan runInputCall
	runInputOut []byte
	runInputErr error
}

// runInputCall is one recorded Deps.RunInput invocation.
type runInputCall struct {
	argv  []string
	input string
}

// recordRun builds the Deps.Run both harnesses inject: it reports the argv and
// answers with the test's canned result, so no test in this package ever starts
// a daemon, touches systemd or depends on what is installed on the machine.
func (h *harness) recordRun() func(context.Context, []string) ([]byte, error) {
	return func(_ context.Context, argv []string) ([]byte, error) {
		h.ran <- append([]string(nil), argv...)
		return h.runOut, h.runErr
	}
}

// recordRunInput is recordRun for the stdin-taking fixes.
func (h *harness) recordRunInput() func(context.Context, []string, string) ([]byte, error) {
	return func(_ context.Context, argv []string, input string) ([]byte, error) {
		h.ranInput <- runInputCall{argv: append([]string(nil), argv...), input: input}
		return h.runInputOut, h.runInputErr
	}
}

// waitRanInput blocks until the fix handler runs a stdin-taking command.
func (h *harness) waitRanInput(t *testing.T) runInputCall {
	t.Helper()
	select {
	case call := <-h.ranInput:
		return call
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for the stdin fix command to run")
	}
	return runInputCall{}
}

// waitRan blocks until the fix handler runs a command and returns its argv.
func (h *harness) waitRan(t *testing.T) []string {
	t.Helper()
	select {
	case argv := <-h.ran:
		return argv
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for the fix command to run")
	}
	return nil
}

// assertNothingRan fails if any command was executed, which is what pins a
// rejected fix as rejected rather than merely unreported.
func (h *harness) assertNothingRan(t *testing.T) {
	t.Helper()
	select {
	case argv := <-h.ran:
		t.Fatalf("handler ran %v; a fix it refused must execute nothing at all", argv)
	default:
	}
}

// newHarness registers the handlers over idx, resolving every backend name to
// store.
func newHarness(t *testing.T, idx Index, store *fakeStore) *harness {
	t.Helper()
	return newHarnessOpen(t, idx, store, func(string) (secrets.Store, error) {
		if store == nil {
			return nil, errors.New("no store")
		}
		return store, nil
	})
}

// newMultiHarness is newHarness with one vault per backend name, for the
// multi-manager tests: it is the only way to prove which manager a copy read
// from or an add wrote to, since a single shared store would answer for all of
// them. An unconfigured name fails the open.
func newMultiHarness(t *testing.T, idx Index, stores map[string]*fakeStore) *harness {
	t.Helper()
	return newHarnessOpen(t, idx, nil, func(name string) (secrets.Store, error) {
		s, ok := stores[name]
		if !ok {
			return nil, fmt.Errorf("no store for %q", name)
		}
		return s, nil
	})
}

// newHarnessOpen is the shared body of the two above: store is only kept for the
// tests that reach for h.store, while open is what the handlers actually resolve
// backend names through.
func newHarnessOpen(t *testing.T, idx Index, store *fakeStore, open func(string) (secrets.Store, error)) *harness {
	t.Helper()
	h := &harness{
		disp:      launch.NewDispatcher(),
		store:     store,
		clock:     newTestClock(fixedUnix),
		indexPath: filepath.Join(t.TempDir(), "totp.json"),
		copied:    make(chan string, 4),
		notified:  make(chan string, 4),
		ran:       make(chan []string, 4),
		ranInput:  make(chan runInputCall, 4),
	}
	if err := SaveIndex(h.indexPath, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	RegisterHandlersWith(h.disp, Deps{
		IndexPath: h.indexPath,
		OpenStore: open,
		Copy:      func(text string) error { h.copied <- text; return nil },
		Now:       h.clock.now,
		Notify:    func(msg string) { h.notified <- msg },
		Run:       h.recordRun(),
		RunInput:  h.recordRunInput(),
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

// gatedStore is a blocking store whose Set parks until the test releases it.
// It exists for one scenario nothing else can stage: a detached add sitting
// inside store.Set — on a real keyring, an unlock prompt nobody has answered
// yet — while the rest of banshee rewrites totp.json underneath it.
type gatedStore struct {
	*fakeStore
	entered chan struct{}
	release chan struct{}
}

func (g *gatedStore) Set(ctx context.Context, key, value string, cred secrets.Credential) error {
	close(g.entered)
	<-g.release
	return g.fakeStore.Set(ctx, key, value, cred)
}

// TestAddDetachedRereadsIndexAfterBlockingWrite is the data-loss guard on the
// detached add path: the index it saves must be the one on disk now, not the
// snapshot it loaded before the write parked. SaveIndex merges only the keys
// this build does not own — an omitempty field at its zero value *deletes* its
// key — so writing the stale snapshot back would strip a secrets manager
// configured meanwhile out of "backends" (silently unrouting every entry that
// names it) and drop any entry added meanwhile from the list while its seed
// stayed in the vault.
func TestAddDetachedRereadsIndexAfterBlockingWrite(t *testing.T) {
	inner := newFakeStore("keyring")
	inner.blocking = true
	gate := &gatedStore{fakeStore: inner, entered: make(chan struct{}), release: make(chan struct{})}
	h := newHarnessOpen(t, Index{V: IndexVersion, Backend: "keyring"}, inner,
		func(string) (secrets.Store, error) { return gate, nil })

	values := map[string]string{"name": "github", "secret": testSeed}
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	select {
	case <-gate.entered:
	case <-time.After(asyncWait):
		t.Fatal("timed out waiting for the detached add to reach the backend write")
	}

	// What the user does while the unlock prompt is up: configures a second
	// manager (appendBackend) and adds a code to it.
	meanwhile := h.index(t)
	meanwhile.AddBackend("plaintext")
	if err := meanwhile.Add(Entry{Name: "gitlab", Backend: "plaintext"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := SaveIndex(h.indexPath, meanwhile); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	close(gate.release)

	if msg := h.waitNotified(t); !strings.Contains(msg, "github") {
		t.Errorf("notification %q does not name the entry", msg)
	}
	got := h.index(t)
	if want := []string{"keyring", "plaintext"}; !reflect.DeepEqual(got.Configured(), want) {
		t.Errorf("configured managers = %v, want %v", got.Configured(), want)
	}
	for _, name := range []string{"github", "gitlab"} {
		if _, ok := got.Find(name); !ok {
			t.Errorf("index lost the %q entry", name)
		}
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
	return newWizardHarnessOpen(t, idx, store, func(string) (secrets.Store, error) {
		if store == nil {
			return nil, errors.New("no store")
		}
		return store, nil
	})
}

// newMultiWizardHarness is newWizardHarness with one vault per backend name; see
// newMultiHarness for why the multi-manager tests need it.
func newMultiWizardHarness(t *testing.T, idx Index, stores map[string]*fakeStore) *wizardHarness {
	t.Helper()
	return newWizardHarnessOpen(t, idx, nil, func(name string) (secrets.Store, error) {
		s, ok := stores[name]
		if !ok {
			return nil, fmt.Errorf("no store for %q", name)
		}
		return s, nil
	})
}

// newWizardHarnessOpen is the shared body of the two above.
func newWizardHarnessOpen(t *testing.T, idx Index, store *fakeStore, open func(string) (secrets.Store, error)) *wizardHarness {
	t.Helper()
	h := &wizardHarness{
		harness: &harness{
			disp:      launch.NewDispatcher(),
			store:     store,
			clock:     newTestClock(fixedUnix),
			indexPath: filepath.Join(t.TempDir(), "totp.json"),
			copied:    make(chan string, 4),
			notified:  make(chan string, 4),
			ran:       make(chan []string, 4),
			ranInput:  make(chan runInputCall, 4),
		},
		state:    &SetupState{},
		reopened: make(chan string, 4),
	}
	if err := SaveIndex(h.indexPath, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	RegisterHandlersWith(h.disp, Deps{
		IndexPath: h.indexPath,
		OpenStore: open,
		Copy:      func(text string) error { h.copied <- text; return nil },
		Now:       h.clock.now,
		Notify:    func(msg string) { h.notified <- msg },
		Run:       h.recordRun(),
		RunInput:  h.recordRunInput(),
		State:     h.state,
		Reopen:    func(q string) { h.reopened <- q },
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

	backend, msg, _, ok := h.state.Snapshot()
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
	if _, _, _, ok := h.state.Snapshot(); ok {
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
	if backend, msg, _, ok := h.state.Snapshot(); ok {
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
	if _, _, _, ok := h.state.Snapshot(); ok {
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
	backend, msg, _, ok := h.state.Snapshot()
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
//
// Both cases go through the copy path deliberately. A failure raised by an
// explicit setup attempt is reported as one (reportSetupFailure) and renders the
// wizard whether or not the manager is configured — configuring it is precisely
// what failed, see TestSetupSecondManagerOpensWizard — so "the provider would
// discard this" now only arises for ordinary use of a manager the index no
// longer lists.
func TestUnusableFailureFallsBackToToast(t *testing.T) {
	tests := []struct {
		name string
		// idx is the index as it stands when the detached read finally fails.
		idx Index
		// wireState wires the failure record; false is the front-end the Deps
		// godoc describes, which keeps the toast behavior.
		wireState bool
	}{
		{
			// The entry names a manager the user has since configured away by
			// hand, so a diagnosis against it describes nothing they would be
			// using and the provider's gate would drop it.
			name: "the failing manager is not configured any more",
			idx: Index{V: IndexVersion, Backend: "plaintext", Entries: []Entry{
				{Name: "github", Backend: "keyring"},
			}},
			wireState: true,
		},
		{
			name:      "no failure record is wired",
			idx:       twoEntryIndex(),
			wireState: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("keyring")
			store.getErr = errors.New("collection is locked")
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

			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}); err != nil {
				t.Fatalf("Dispatch returned %v, want the read to run detached", err)
			}
			if msg := h.waitNotified(t); !strings.Contains(msg, "could not read") {
				t.Errorf("notification %q does not carry the backend's diagnosis", msg)
			}
			h.assertNotReopened(t)
			if _, _, _, ok := h.state.Snapshot(); ok {
				t.Error("a failure the provider would discard was recorded anyway")
			}
		})
	}
}

// TestSetupSecondManagerOpensWizard is the other side of that rule: adding a
// manager probes one that is, by definition, not configured yet. Routing that
// failure to a toast would leave the user with a message that vanishes and no
// retry — the dead end the wizard exists to replace.
func TestSetupSecondManagerOpensWizard(t *testing.T) {
	keyring := newFakeStore("keyring")
	keyring.blocking = true
	keyring.setErr = errors.New("collection is locked")
	plaintext := newFakeStore("plaintext")
	h := newMultiWizardHarness(t,
		Index{V: IndexVersion, Backend: "plaintext", Entries: []Entry{{Name: "github"}}},
		map[string]*fakeStore{"keyring": keyring, "plaintext": plaintext})

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch returned %v, want the probe to run detached", err)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger so the wizard rows render", q)
	}
	h.assertQuiet(t)
	backend, msg, fromSetup, ok := h.state.Snapshot()
	if !ok || backend != "keyring" {
		t.Fatalf("recorded failure = (%q, %v), want one against the manager being added", backend, ok)
	}
	if !fromSetup {
		t.Error("the failure is not marked as a setup attempt, so the provider's gate would discard it")
	}
	if !strings.Contains(msg, "not usable") {
		t.Errorf("recorded message %q does not carry the probe's own diagnosis", msg)
	}
	// The manager the user already had must survive a failed attempt to add
	// another one.
	if got := h.index(t).Configured(); !reflect.DeepEqual(got, []string{"plaintext"}) {
		t.Errorf("configured = %v, want the failed probe to have changed nothing", got)
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
				if backend, _, _, ok := h.state.Snapshot(); !ok || backend != wizardStaleBackend {
					t.Errorf("recorded failure = (%q, %v), want the earlier one left alone", backend, ok)
				}
				return
			}
			if q := h.waitReopened(t); q != "totp" {
				t.Errorf("reopened on %q, want the provider's trigger", q)
			}
			h.assertQuiet(t)
			backend, msg, _, ok := h.state.Snapshot()
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
			backend, msg, _, ok := h.state.Snapshot()
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
			if _, _, _, ok := h.state.Snapshot(); ok {
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
		if _, _, _, ok := h.state.Snapshot(); ok {
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

// daemonFixArgv is the command the keyring's one fix row stands for. It is
// written out here rather than read back out of wizardFixes so a test that
// dispatches the row's Action proves the whole chain — row id, table lookup,
// Deps.Run — lands on this exact argv.
var daemonFixArgv = []string{"systemctl", "--user", "start", "gnome-keyring-daemon"}

// keyringFixAction is the Action the wizard's "Start the Secret Service daemon"
// row emits.
func keyringFixAction() providers.Action {
	return providers.Action{Kind: ActTOTPWizardFix, Argv: []string{"keyring", "keyring:daemon"}}
}

// createFixArgv is the create-keyring fix's command, written out for the same
// reason as daemonFixArgv.
var createFixArgv = []string{"gnome-keyring-daemon", "--unlock"}

// createFixThenArgv is the create fix's follow-up: the daemon restart that
// makes the running Secret Service load the keyring --unlock just wrote.
var createFixThenArgv = []string{"systemctl", "--user", "restart", "gnome-keyring-daemon"}

// createFixAction is what the create-keyring form's Build emits: the fix key
// plus the submitted password riding Values.
func createFixAction(password string) providers.Action {
	return providers.Action{
		Kind:   ActTOTPWizardFix,
		Argv:   []string{"keyring", "keyring:create"},
		Values: map[string]string{"password": password},
	}
}

// TestWizardFixStdinPipesPassword proves the create-keyring fix sends the
// submitted password to the command's standard input — never argv — and then
// finishes setup exactly like any other fix that worked.
func TestWizardFixStdinPipesPassword(t *testing.T) {
	store := newFakeStore("keyring")
	h := newWizardHarness(t, Index{V: IndexVersion}, store)
	h.state.Fail("keyring", errors.New("no default collection"))

	if err := h.disp.Dispatch(createFixAction("hunter2")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	call := h.waitRanInput(t)
	if !reflect.DeepEqual(call.argv, createFixArgv) {
		t.Errorf("ran %v, want %v from the fix table", call.argv, createFixArgv)
	}
	if call.input != "hunter2" {
		t.Errorf("stdin = %q, want the submitted password", call.input)
	}
	for _, arg := range call.argv {
		if strings.Contains(arg, "hunter2") {
			t.Fatalf("password appeared on argv: %v", call.argv)
		}
	}
	// The follow-up restart runs second, without stdin, so the running Secret
	// Service loads the keyring --unlock just created.
	if argv := h.waitRan(t); !reflect.DeepEqual(argv, createFixThenArgv) {
		t.Errorf("follow-up ran %v, want %v", argv, createFixThenArgv)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	h.assertQuiet(t)
	if _, _, _, ok := h.state.Snapshot(); ok {
		t.Error("failure still recorded after a create that worked")
	}
	if got := h.index(t).Backend; got != "keyring" {
		t.Errorf("persisted backend = %q, want setup finished", got)
	}
}

// TestWizardFixStdinPasswordVariants pins the edge shapes of the piped
// password: absent Values means the empty password (a legitimate blank-keyring
// choice), and a failure must never leak the password into the recorded
// diagnosis the wizard renders on screen.
func TestWizardFixStdinPasswordVariants(t *testing.T) {
	t.Run("nil Values pipes the empty password", func(t *testing.T) {
		store := newFakeStore("keyring")
		h := newWizardHarness(t, Index{V: IndexVersion}, store)
		action := createFixAction("")
		action.Values = nil
		if err := h.disp.Dispatch(action); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if call := h.waitRanInput(t); call.input != "" {
			t.Errorf("stdin = %q, want empty", call.input)
		}
		h.waitReopened(t)
	})

	t.Run("a failed follow-up restart re-arms the wizard", func(t *testing.T) {
		store := newFakeStore("keyring")
		h := newWizardHarness(t, Index{V: IndexVersion}, store)
		// The unlock succeeds (runInputErr nil) but the daemon restart fails.
		h.runErr = errors.New("exit status 5")
		h.runOut = []byte("Failed to restart gnome-keyring-daemon.service")
		if err := h.disp.Dispatch(createFixAction("hunter2")); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		h.waitRanInput(t)
		if argv := h.waitRan(t); !reflect.DeepEqual(argv, createFixThenArgv) {
			t.Errorf("follow-up ran %v, want %v", argv, createFixThenArgv)
		}
		if q := h.waitReopened(t); q != "totp" {
			t.Errorf("reopened on %q", q)
		}
		_, msg, _, ok := h.state.Snapshot()
		if !ok || !strings.Contains(msg, "could not create or unlock the login keyring") {
			t.Errorf("Snapshot = (%q, %v), want the create failMsg recorded", msg, ok)
		}
		if got := h.index(t).Backend; got != "" {
			t.Errorf("persisted backend = %q, want untouched", got)
		}
	})

	t.Run("a failed create never leaks the password", func(t *testing.T) {
		store := newFakeStore("keyring")
		h := newWizardHarness(t, Index{V: IndexVersion}, store)
		h.runInputErr = errors.New("exit status 1")
		h.runInputOut = []byte("couldn't unlock the login keyring")
		if err := h.disp.Dispatch(createFixAction("hunter2")); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		h.waitRanInput(t)
		if q := h.waitReopened(t); q != "totp" {
			t.Errorf("reopened on %q", q)
		}
		backend, msg, _, ok := h.state.Snapshot()
		if !ok || backend != "keyring" {
			t.Fatalf("Snapshot = (%q, %q, %v), want a keyring failure recorded", backend, msg, ok)
		}
		if !strings.Contains(msg, "could not create or unlock the login keyring") {
			t.Errorf("message %q lacks the fix's failMsg", msg)
		}
		if strings.Contains(msg, "hunter2") {
			t.Fatalf("recorded diagnosis contains the password: %q", msg)
		}
		if got := h.index(t).Backend; got != "" {
			t.Errorf("persisted backend = %q, want untouched after a failed create", got)
		}
	})
}

// TestWizardFixSuccessCompletesSetup is the headline of the fix row: pressing
// Enter runs the repair and then finishes setup by itself. The user asked for a
// working keyring, not for a command to be run, so a fix that worked must leave
// the backend probed, persisted and the wizard gone — being handed a "now press
// Retry" row would be banshee asking them to do its remaining half of the job.
//
// The store is deliberately non-blocking: the fix path detaches because it shells
// out, not because of anything the backend does, and Dispatch must return
// immediately either way.
func TestWizardFixSuccessCompletesSetup(t *testing.T) {
	store := newFakeStore("keyring")
	h := newWizardHarness(t, Index{V: IndexVersion}, store)
	h.state.Fail(wizardStaleBackend, errors.New("stale failure"))

	if err := h.disp.Dispatch(keyringFixAction()); err != nil {
		t.Fatalf("Dispatch returned %v, want the fix to run detached", err)
	}
	if argv := h.waitRan(t); !reflect.DeepEqual(argv, daemonFixArgv) {
		t.Errorf("ran %v, want %v from the fix table", argv, daemonFixArgv)
	}
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	h.assertQuiet(t)

	if backend, msg, _, ok := h.state.Snapshot(); ok {
		t.Errorf("recorded failure = (%q, %q) after a fix that worked, want the wizard retired", backend, msg)
	}
	if got := h.index(t).Backend; got != "keyring" {
		t.Errorf("persisted backend = %q, want the fix to have finished setup", got)
	}
	// The probe is the only thing that deletes a key, so a delete is proof the
	// fix retried setup rather than just persisting a backend it never checked.
	if got := store.deletedKeys(); !reflect.DeepEqual(got, []string{probeKey}) {
		t.Errorf("deleted keys = %v, want the probe to have run and cleaned up", got)
	}
}

// TestWizardFixCommandFails covers the repair that does not take. The user is
// looking at the wizard, so the new diagnosis belongs in it — with the command's
// own output, which is the sentence that points at the next row down ("Unit not
// found" means install it) — and never as a toast behind the launcher.
func TestWizardFixCommandFails(t *testing.T) {
	tests := []struct {
		name    string
		runOut  []byte
		wantSub string
	}{
		{
			name:    "the command's own complaint is carried",
			runOut:  []byte("Failed to start gnome-keyring-daemon.service: Unit not found.\n"),
			wantSub: "Unit not found",
		},
		{
			name:    "a silent command still names the fix",
			runOut:  nil,
			wantSub: "could not start the Secret Service daemon",
		},
		{
			// A daemon that dumps a log into stderr must not turn a result
			// subtitle into a scrollback buffer.
			name:    "a flood of output is bounded",
			runOut:  []byte(strings.Repeat("boom ", 4000)),
			wantSub: "boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("keyring")
			h := newWizardHarness(t, Index{V: IndexVersion}, store)
			h.runOut = tt.runOut
			h.runErr = errors.New("exit status 1")

			if err := h.disp.Dispatch(keyringFixAction()); err != nil {
				t.Fatalf("Dispatch returned %v, want the failure reported as wizard rows", err)
			}
			h.waitRan(t)
			if q := h.waitReopened(t); q != "totp" {
				t.Errorf("reopened on %q, want the provider's trigger", q)
			}
			h.assertQuiet(t)

			backend, msg, _, ok := h.state.Snapshot()
			if !ok || backend != "keyring" {
				t.Fatalf("recorded failure = (%q, %v), want one against the keyring", backend, ok)
			}
			if !strings.Contains(msg, "could not start the Secret Service daemon") {
				t.Errorf("recorded message %q does not head with the fix's own failMsg", msg)
			}
			if !strings.Contains(msg, "exit status 1") {
				t.Errorf("recorded message %q drops the command's error", msg)
			}
			if !strings.Contains(msg, tt.wantSub) {
				t.Errorf("recorded message %q does not mention %q", msg, tt.wantSub)
			}
			if len(msg) > 2*fixOutputLimit {
				t.Errorf("recorded message is %d bytes, want the output folded in bounded by %d", len(msg), fixOutputLimit)
			}
			if got := h.index(t).Backend; got != "" {
				t.Errorf("persisted backend = %q, want the index untouched by a failed fix", got)
			}
			if got := store.deletedKeys(); len(got) != 0 {
				t.Errorf("probe ran (%v) after the fix failed; there was nothing to re-probe", got)
			}
		})
	}
}

// TestWizardFixProbeStillFails is the fix that ran cleanly and changed nothing —
// systemctl reports success, the Secret Service is still refusing writes. The
// user must get the backend's own fresh diagnosis, not a silent success, and the
// index must stay untouched so the setup rows still come back.
func TestWizardFixProbeStillFails(t *testing.T) {
	store := newFakeStore("keyring")
	store.setErr = errors.New("collection is locked")
	h := newWizardHarness(t, Index{V: IndexVersion}, store)

	if err := h.disp.Dispatch(keyringFixAction()); err != nil {
		t.Fatalf("Dispatch returned %v, want the fix to run detached", err)
	}
	h.waitRan(t)
	if q := h.waitReopened(t); q != "totp" {
		t.Errorf("reopened on %q, want the provider's trigger", q)
	}
	h.assertQuiet(t)

	backend, msg, _, ok := h.state.Snapshot()
	if !ok || backend != "keyring" {
		t.Fatalf("recorded failure = (%q, %v), want one against the keyring", backend, ok)
	}
	if !strings.Contains(msg, "not usable") {
		t.Errorf("recorded message %q does not carry the probe's own diagnosis", msg)
	}
	if got := h.index(t).Backend; got != "" {
		t.Errorf("persisted backend = %q, want the index untouched by a failed probe", got)
	}
}

// TestWizardFixRejections is the security gate in test form: the Action carries a
// lookup key, so anything that is not a pair this package published must be
// refused synchronously and execute nothing. A forged Result — a rogue plugin, a
// hand-built IPC payload — gets an error, never a process.
func TestWizardFixRejections(t *testing.T) {
	tests := []struct {
		name string
		argv []string
	}{
		{name: "no argv at all", argv: nil},
		{name: "a backend with no fix id", argv: []string{"keyring"}},
		{name: "an id nobody defined", argv: []string{"keyring", "keyring:rm-rf"}},
		{name: "an id belonging to another backend", argv: []string{"plaintext", "keyring:daemon"}},
		{name: "blank halves", argv: []string{"  ", "  "}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newWizardHarness(t, Index{V: IndexVersion}, newFakeStore("keyring"))
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPWizardFix, Argv: tt.argv})
			if err == nil {
				t.Fatal("Dispatch succeeded, want a synchronous error")
			}
			if !strings.Contains(err.Error(), "totp-wizard-fix") {
				t.Errorf("error %q does not name the action kind that refused it", err)
			}
			h.assertNothingRan(t)
			h.assertNotReopened(t)
		})
	}
}

// TestWizardFixUnwired keeps the promise Deps.State and Deps.Reopen make to a
// front-end that wires neither: the wizard is off, so both outcomes of a fix have
// to reach the user as a toast rather than vanishing.
func TestWizardFixUnwired(t *testing.T) {
	t.Run("the command fails", func(t *testing.T) {
		h := newHarness(t, Index{V: IndexVersion}, newFakeStore("keyring"))
		h.runErr = errors.New("exit status 1")

		if err := h.disp.Dispatch(keyringFixAction()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		h.waitRan(t)
		if msg := h.waitNotified(t); !strings.Contains(msg, "could not start the Secret Service daemon") {
			t.Errorf("notification %q does not carry the fix's diagnosis", msg)
		}
	})
	t.Run("the command works", func(t *testing.T) {
		store := newFakeStore("keyring")
		h := newHarness(t, Index{V: IndexVersion}, store)

		if err := h.disp.Dispatch(keyringFixAction()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		h.waitRan(t)
		if msg := h.waitNotified(t); !strings.Contains(msg, "will be stored") {
			t.Errorf("notification %q does not confirm the completed setup", msg)
		}
		if got := h.index(t).Backend; got != "keyring" {
			t.Errorf("persisted backend = %q, want the fix to have finished setup", got)
		}
	})
}

// TestCopyRoutesEntryBackend is the read half of per-entry storage: the seed
// must be fetched from the manager the entry names, and from the index default
// for the entries written before a second manager existed. Reading the default
// for everything would silently report every entry in the second manager as
// having no secret stored.
func TestCopyRoutesEntryBackend(t *testing.T) {
	tests := []struct {
		name string
		// entry is the index entry to copy, whose Backend is the axis under test.
		entry Entry
		// wantStore is the fake vault that must have been asked.
		wantStore string
	}{
		{name: "no backend uses the default", entry: Entry{Name: "github"}, wantStore: "keyring"},
		{name: "an explicit backend is honored", entry: Entry{Name: "github", Backend: "plaintext"}, wantStore: "plaintext"},
		{name: "an explicit default is honored too", entry: Entry{Name: "github", Backend: "keyring"}, wantStore: "keyring"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stores := map[string]*fakeStore{"keyring": newFakeStore("keyring"), "plaintext": newFakeStore("plaintext")}
			stores[tt.wantStore].values["totp/github"] = testSeed
			idx := Index{V: IndexVersion, Backend: "keyring", Backends: []string{"plaintext"}, Entries: []Entry{tt.entry}}
			h := newMultiHarness(t, idx, stores)

			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}}); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got := h.waitCopied(t); got != codeAtFixed {
				t.Errorf("copied %q, want %q", got, codeAtFixed)
			}
			for name, s := range stores {
				reads := len(s.creds)
				if name == tt.wantStore && reads != 1 {
					t.Errorf("%s was read %d times, want the entry's manager to answer once", name, reads)
				}
				if name != tt.wantStore && reads != 0 {
					t.Errorf("%s was read %d times, want the other manager left alone", name, reads)
				}
			}
		})
	}
}

// TestAddStoresChosenBackend is the write half: the seed goes into the manager
// the form's Storage dropdown chose, and the choice is recorded on the entry only
// when it differs from the default — a single-manager user's index must not grow
// a "backend" key per entry just because the field now exists.
func TestAddStoresChosenBackend(t *testing.T) {
	tests := []struct {
		name string
		idx  Index
		// chosen is Values["backend"] as the dropdown would submit it; empty is
		// the single-manager form, which has no dropdown at all.
		chosen       string
		wantStore    string
		wantRecorded string
	}{
		{
			name:      "no choice uses the default and records nothing",
			idx:       Index{V: IndexVersion, Backend: "keyring", Backends: []string{"plaintext"}},
			chosen:    "",
			wantStore: "keyring",
		},
		{
			name:      "choosing the default records nothing",
			idx:       Index{V: IndexVersion, Backend: "keyring", Backends: []string{"plaintext"}},
			chosen:    "keyring",
			wantStore: "keyring",
		},
		{
			name:         "choosing the second manager records it",
			idx:          Index{V: IndexVersion, Backend: "keyring", Backends: []string{"plaintext"}},
			chosen:       "plaintext",
			wantStore:    "plaintext",
			wantRecorded: "plaintext",
		},
		{
			name:      "a single manager stays free of the key",
			idx:       Index{V: IndexVersion, Backend: "plaintext"},
			chosen:    "",
			wantStore: "plaintext",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stores := map[string]*fakeStore{}
			for _, name := range tt.idx.Configured() {
				stores[name] = newFakeStore(name)
			}
			h := newMultiHarness(t, tt.idx, stores)

			values := map[string]string{"name": "github", "secret": testSeed}
			if tt.chosen != "" {
				values["backend"] = tt.chosen
			}
			if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values}); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			for name, s := range stores {
				_, stored := s.get("totp/github")
				if want := name == tt.wantStore; stored != want {
					t.Errorf("%s holds the seed = %v, want %v", name, stored, want)
				}
			}
			entry, ok := h.index(t).Find("github")
			if !ok {
				t.Fatal("the index has no entry after a successful add")
			}
			if entry.Backend != tt.wantRecorded {
				t.Errorf("entry backend = %q, want %q", entry.Backend, tt.wantRecorded)
			}
			// The rendered rows read through BackendOr, so the recorded value and
			// the vault that holds the seed have to agree whichever way round it
			// was stored.
			if got := entry.BackendOr(h.index(t).DefaultBackend()); got != tt.wantStore {
				t.Errorf("BackendOr = %q, want the manager the seed went to (%q)", got, tt.wantStore)
			}
		})
	}
}

// TestAddRejectsUnconfiguredBackend is the validation on Values["backend"]: an
// Action can be minted anywhere (a plugin, a hand-built IPC payload), and writing
// a seed into a vault the user never configured would put it somewhere they would
// never think to look — and somewhere the provider would never read it back from.
func TestAddRejectsUnconfiguredBackend(t *testing.T) {
	tests := []struct {
		name    string
		chosen  string
		wantErr string
	}{
		{name: "a manager that exists but is not configured", chosen: "keyring", wantErr: "not a configured secrets manager"},
		{name: "a name no backend answers to", chosen: "vault", wantErr: "not a configured secrets manager"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plaintext := newFakeStore("plaintext")
			h := newMultiHarness(t, Index{V: IndexVersion, Backend: "plaintext"},
				map[string]*fakeStore{"plaintext": plaintext, "keyring": newFakeStore("keyring")})

			values := map[string]string{"name": "github", "secret": testSeed, "backend": tt.chosen}
			err := h.disp.Dispatch(providers.Action{Kind: ActTOTPAdd, Values: values})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Dispatch error = %v, want one mentioning %q", err, tt.wantErr)
			}
			if len(h.index(t).Entries) != 0 {
				t.Error("a rejected add left metadata behind")
			}
			if _, stored := plaintext.get("totp/github"); stored {
				t.Error("a rejected add wrote the seed to the default manager anyway")
			}
		})
	}
}

// TestAppendBackendCompat is the on-disk compatibility contract of adding a
// second manager, asserted through the real save path because that is where it
// can break: mergeKnown deletes a key whose omitempty field is zero, so the
// AddBackend invariant (legacy "backend" always holds Configured()[0]) is the
// only thing keeping an older banshee able to find any seeds at all. Unknown keys
// have to survive the rewrite for the same reason they always did.
func TestAppendBackendCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp.json")
	const legacy = `{
  "v": 1,
  "backend": "keyring",
  "comment": "hand written",
  "entries": [
    {"name": "github", "note": "work account"}
  ]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	// Adding the manager already configured must be a no-op, twice over.
	for _, name := range []string{"keyring", "plaintext", "plaintext"} {
		idx, err := LoadIndex(path)
		if err != nil {
			t.Fatalf("LoadIndex: %v", err)
		}
		idx.AddBackend(name)
		if err := SaveIndex(path, idx); err != nil {
			t.Fatalf("SaveIndex: %v", err)
		}
	}

	idx, err := LoadIndex(path)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if want := []string{"keyring", "plaintext"}; !reflect.DeepEqual(idx.Configured(), want) {
		t.Errorf("Configured() = %v, want %v (deduped, default first)", idx.Configured(), want)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["backend"] != "keyring" {
		t.Errorf("\"backend\" = %v, want the first manager left in the legacy key an older build reads", raw["backend"])
	}
	if !reflect.DeepEqual(raw["backends"], []any{"plaintext"}) {
		t.Errorf("\"backends\" = %v, want just the second manager", raw["backends"])
	}
	if raw["comment"] != "hand written" {
		t.Errorf("unknown key \"comment\" = %v, want it preserved", raw["comment"])
	}
	entries, ok := raw["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v, want the original one", raw["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["note"] != "work account" {
		t.Errorf("unknown entry key \"note\" = %v, want it preserved", first["note"])
	}
	if _, present := first["backend"]; present {
		t.Errorf("entry gained a \"backend\" key: %v; an entry in the default manager must stay untouched", first)
	}

	t.Run("a single manager writes no backends key at all", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "totp.json")
		var idx Index
		idx.AddBackend("plaintext")
		if err := SaveIndex(path, idx); err != nil {
			t.Fatalf("SaveIndex: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "backends") {
			t.Errorf("single-manager index mentions \"backends\": %s", data)
		}
	})
}

// TestSetupMoreReopens covers the hint row's handler. Its only job is
// navigation, so it must never fail — and with no Reopen wired it has to name the
// query the user could type instead, because a row that silently does nothing is
// worse than no row.
func TestSetupMoreReopens(t *testing.T) {
	t.Run("wired", func(t *testing.T) {
		h := newWizardHarness(t, twoEntryIndex(), stockedStore())
		if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetupMore}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if q := h.waitReopened(t); q != "totp setup" {
			t.Errorf("reopened on %q, want the chooser query", q)
		}
	})
	t.Run("unwired", func(t *testing.T) {
		h := newHarness(t, twoEntryIndex(), stockedStore())
		if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetupMore}); err != nil {
			t.Fatalf("Dispatch with no Reopen: %v", err)
		}
		if msg := h.waitNotified(t); !strings.Contains(msg, "totp setup") {
			t.Errorf("notification %q does not name the query that opens the chooser", msg)
		}
	})
}

// TestSetupAppendsManagers is the accumulating half of setup: a second choice
// must join the first rather than replace it, or every seed in the first manager
// would become unreachable the moment a user tried a second one.
func TestSetupAppendsManagers(t *testing.T) {
	stores := map[string]*fakeStore{"plaintext": newFakeStore("plaintext"), "keyring": newFakeStore("keyring")}
	h := newMultiHarness(t, Index{V: IndexVersion, Backend: "plaintext", Entries: []Entry{{Name: "github"}}}, stores)

	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	idx := h.index(t)
	if want := []string{"plaintext", "keyring"}; !reflect.DeepEqual(idx.Configured(), want) {
		t.Errorf("Configured() = %v, want %v", idx.Configured(), want)
	}
	if idx.Backend != "plaintext" {
		t.Errorf("legacy backend = %q, want the first manager kept there", idx.Backend)
	}
	if _, ok := idx.Find("github"); !ok {
		t.Error("the existing entry was lost by configuring a second manager")
	}
	// Idempotent: the wizard's Retry row and a second pass through the chooser
	// both re-run setup on a manager already configured.
	if err := h.disp.Dispatch(providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if want := []string{"plaintext", "keyring"}; !reflect.DeepEqual(h.index(t).Configured(), want) {
		t.Errorf("Configured() = %v after re-running setup, want %v", h.index(t).Configured(), want)
	}
}

func TestDispatchRejectsUnregisteredKind(t *testing.T) {
	h := newHarness(t, twoEntryIndex(), stockedStore())
	if err := h.disp.Dispatch(providers.Action{Kind: "totp-delete"}); err == nil {
		t.Error("Dispatch of an unregistered kind succeeded, want an error")
	}
}
