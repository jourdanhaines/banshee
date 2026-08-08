package totp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// fixedUnix is the instant every clock-sensitive test runs at. The codes it
// produces for testSeed are hard-coded below rather than recomputed with Code,
// so a regression in the algorithm cannot make these tests agree with it.
const fixedUnix = 1_700_000_000

const (
	testSeed     = "JBSWY3DPEHPK3PXP"
	codeAtFixed  = "324550" // testSeed at fixedUnix
	codeNextStep = "367665" // testSeed at fixedUnix+30
)

// fakeStore is an in-memory secrets.Store. It records every credential it is
// handed so the tests can prove a masked form value reaches the backend, and
// it can be told to fail any operation.
type fakeStore struct {
	name     string
	auth     bool
	blocking bool

	mu      sync.Mutex
	values  map[string]string
	creds   []secrets.Credential
	deleted []string
	getErr  error
	setErr  error
	delErr  error
}

func newFakeStore(name string) *fakeStore {
	return &fakeStore{name: name, values: map[string]string{}}
}

func (f *fakeStore) Name() string { return f.name }

func (f *fakeStore) AuthPerAccess() bool { return f.auth }

// Blocking is false by default: an in-memory map answers immediately, so the
// handlers take their synchronous path unless a test opts into the detached
// one.
func (f *fakeStore) Blocking() bool { return f.blocking }

func (f *fakeStore) Get(ctx context.Context, key string, cred secrets.Credential) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = append(f.creds, cred)
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.values[key]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return v, nil
}

func (f *fakeStore) Set(ctx context.Context, key, value string, cred secrets.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = append(f.creds, cred)
	if f.setErr != nil {
		return f.setErr
	}
	f.values[key] = value
	return nil
}

func (f *fakeStore) Delete(ctx context.Context, key string, cred secrets.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	f.deleted = append(f.deleted, key)
	delete(f.values, key)
	return nil
}

func (f *fakeStore) get(key string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[key]
	return v, ok
}

func (f *fakeStore) lastCred() secrets.Credential {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.creds) == 0 {
		return secrets.Credential{}
	}
	return f.creds[len(f.creds)-1]
}

func (f *fakeStore) deletedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// substringScorer is a deterministic stand-in for fuzzy.Score: a
// case-insensitive substring hit scores 100, everything else misses.
func substringScorer(query, candidate string) (int, bool) {
	if strings.Contains(strings.ToLower(candidate), strings.ToLower(query)) {
		return 100, true
	}
	return 0, false
}

// zeroScorer matches everything at score 0, to prove the provider's own
// threshold (CatTOTP is exempt from the aggregator's) drops non-positive hits.
func zeroScorer(query, candidate string) (int, bool) { return 0, true }

// writeIndex writes idx to a fresh temp file and returns its path.
func writeIndex(t *testing.T, idx Index) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "totp.json")
	if err := SaveIndex(path, idx); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	return path
}

// fixedNow returns the frozen clock used by the provider tests.
func fixedNow() time.Time { return time.Unix(fixedUnix, 0) }

// newProvider builds a provider over idx with store behind every backend name.
func newProvider(t *testing.T, idx Index, store secrets.Store, score Scorer) *Provider {
	t.Helper()
	return New(score,
		WithIndexPath(writeIndex(t, idx)),
		WithOpenStore(func(string) (secrets.Store, error) { return store, nil }),
		WithNow(fixedNow),
	)
}

func ids(res []providers.Result) []string {
	var out []string
	for _, r := range res {
		out = append(out, r.ID)
	}
	return out
}

func TestQueryNoBackend(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{name: "empty query stays silent", query: "", wantIDs: nil},
		{name: "whitespace stays silent", query: "   ", wantIDs: nil},
		{name: "untriggered stays silent", query: "github", wantIDs: nil},
		{
			name:    "totp trigger offers backends",
			query:   "totp",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
		{
			name:    "otp trigger offers backends",
			query:   "otp",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
		{
			name:    "trigger is case-insensitive",
			query:   "TOTP",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
		{
			name:    "trigger with a filter still offers backends",
			query:   "totp github",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(t, Index{V: IndexVersion}, newFakeStore("plaintext"), substringScorer)
			res, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query(%q) error: %v", tt.query, err)
			}
			if got := ids(res); !reflect.DeepEqual(got, tt.wantIDs) {
				t.Errorf("Query(%q) ids = %v, want %v", tt.query, got, tt.wantIDs)
			}
		})
	}
}

func TestQuerySetupRowShape(t *testing.T) {
	p := newProvider(t, Index{V: IndexVersion}, newFakeStore("plaintext"), substringScorer)
	res, err := p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	want := []providers.Result{
		{
			ID:       "totp:setup:keyring",
			Title:    "Use OS keyring (local)",
			Subtitle: "Store seeds in the system Secret Service",
			Icon:     providers.Icon{ThemeName: iconEntry},
			Category: providers.CatTOTP,
			Score:    TriggerScore,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"keyring"}},
		},
		{
			ID:       "totp:setup:plaintext",
			Title:    "Use plaintext file (local, not recommended)",
			Subtitle: "Store seeds unencrypted, readable by anything running as you",
			Icon:     providers.Icon{ThemeName: iconEntry},
			Category: providers.CatTOTP,
			Score:    TriggerScore,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"plaintext"}},
		},
		{
			ID:       "totp:setup:nimbus",
			Title:    "Nimbus (cloud — coming soon)",
			Subtitle: "Not available yet",
			Icon:     providers.Icon{ThemeName: iconEntry},
			Category: providers.CatTOTP,
			Score:    TriggerScore,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{"nimbus"}},
		},
	}
	if !reflect.DeepEqual(res, want) {
		t.Errorf("Query() = %+v, want %+v", res, want)
	}
}

// twoEntryIndex is the fixture the listing tests share: two entries whose
// names overlap, so a filter can select one of them.
func twoEntryIndex() Index {
	return Index{
		V:       IndexVersion,
		Backend: "plaintext",
		Entries: []Entry{{Name: "github"}, {Name: "gitlab"}},
	}
}

func stockedStore() *fakeStore {
	s := newFakeStore("plaintext")
	s.values["totp/github"] = testSeed
	s.values["totp/gitlab"] = testSeed
	return s
}

func TestQueryListing(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		scorer  Scorer
		wantIDs []string
	}{
		{name: "empty query stays silent", query: "", scorer: substringScorer, wantIDs: nil},
		{
			name:    "bare trigger lists everything with add last",
			query:   "totp",
			scorer:  substringScorer,
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add"},
		},
		{
			name:    "trigger remainder filters",
			query:   "totp lab",
			scorer:  substringScorer,
			wantIDs: []string{"totp:gitlab", "totp:add"},
		},
		{
			name:    "trigger remainder matching nothing keeps add row",
			query:   "otp nothing",
			scorer:  substringScorer,
			wantIDs: []string{"totp:add"},
		},
		{
			name:    "untriggered fuzzy match has no add row",
			query:   "github",
			scorer:  substringScorer,
			wantIDs: []string{"totp:github"},
		},
		{
			name:    "untriggered miss returns nothing",
			query:   "unrelated",
			scorer:  substringScorer,
			wantIDs: nil,
		},
		{
			name:    "untriggered non-positive score is thresholded away",
			query:   "github",
			scorer:  zeroScorer,
			wantIDs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(t, twoEntryIndex(), stockedStore(), tt.scorer)
			res, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query(%q) error: %v", tt.query, err)
			}
			if got := ids(res); !reflect.DeepEqual(got, tt.wantIDs) {
				t.Errorf("Query(%q) ids = %v, want %v", tt.query, got, tt.wantIDs)
			}
		})
	}
}

func TestQueryEntryRowShape(t *testing.T) {
	p := newProvider(t, twoEntryIndex(), stockedStore(), substringScorer)
	res, err := p.Query(context.Background(), "github")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Query() rows = %d, want 1", len(res))
	}
	want := providers.Result{
		ID:       "totp:github",
		Title:    "github",
		Subtitle: "324 550",
		Icon:     providers.Icon{ThemeName: "security-high-symbolic"},
		Category: providers.CatTOTP,
		Score:    100,
		Action:   providers.Action{Kind: ActTOTPCopy, Argv: []string{"github"}},
		Expiry:   time.Unix(fixedUnix+10, 0),
	}
	if !reflect.DeepEqual(res[0], want) {
		t.Errorf("Query() = %+v, want %+v", res[0], want)
	}
}

func TestQueryExpiryFollowsPeriod(t *testing.T) {
	tests := []struct {
		name   string
		period int
		want   time.Time
	}{
		{name: "default period", period: 0, want: time.Unix(fixedUnix+10, 0)},
		{name: "explicit 30s", period: 30, want: time.Unix(fixedUnix+10, 0)},
		{name: "60s", period: 60, want: time.Unix(fixedUnix+40, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := Index{
				V:       IndexVersion,
				Backend: "plaintext",
				Entries: []Entry{{Name: "github", Period: tt.period}},
			}
			p := newProvider(t, idx, stockedStore(), substringScorer)
			res, err := p.Query(context.Background(), "totp")
			if err != nil {
				t.Fatalf("Query() error: %v", err)
			}
			if !res[0].Expiry.Equal(tt.want) {
				t.Errorf("Expiry = %v, want %v", res[0].Expiry, tt.want)
			}
		})
	}
}

func TestQueryAuthBackendRow(t *testing.T) {
	store := stockedStore()
	store.auth = true
	p := newProvider(t, twoEntryIndex(), store, substringScorer)
	res, err := p.Query(context.Background(), "github")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("Query() rows = %d, want 1", len(res))
	}
	row := res[0]
	if row.Subtitle != "Enter to unlock and copy" {
		t.Errorf("Subtitle = %q, want the unlock prompt", row.Subtitle)
	}
	if !row.Expiry.IsZero() {
		t.Errorf("Expiry = %v, want zero (no code was rendered)", row.Expiry)
	}
	if row.Form == nil {
		t.Fatal("Form = nil, want a credential form")
	}
	if row.Form.Title != "Unlock github" {
		t.Errorf("Form.Title = %q, want %q", row.Form.Title, "Unlock github")
	}
	wantFields := []providers.FormField{{Key: "credential", Label: "Password", Required: true, Secret: true}}
	if !reflect.DeepEqual(row.Form.Fields, wantFields) {
		t.Errorf("Form.Fields = %+v, want %+v", row.Form.Fields, wantFields)
	}
	if _, err := row.Form.Build(map[string]string{"credential": "  "}); err == nil {
		t.Error("Build with a blank password succeeded, want an error")
	}
	got, err := row.Form.Build(map[string]string{"credential": "hunter2"})
	if err != nil {
		t.Fatalf("Build error: %v", err)
	}
	want := providers.Action{
		Kind:   ActTOTPCopy,
		Argv:   []string{"github"},
		Values: map[string]string{"credential": "hunter2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Build() = %+v, want %+v", got, want)
	}
	// The auth backend must never have been read while rendering rows.
	if len(store.creds) != 0 {
		t.Errorf("store was read %d times while rendering, want 0", len(store.creds))
	}
}

func TestQueryAddRowForm(t *testing.T) {
	tests := []struct {
		name       string
		auth       bool
		wantFields []providers.FormField
	}{
		{
			name: "local backend",
			auth: false,
			wantFields: []providers.FormField{
				{Key: "name", Label: "Name", Placeholder: "github", Required: true},
				{Key: "secret", Label: "Secret", Placeholder: "base32 seed or otpauth:// URI", Required: true, Secret: true},
			},
		},
		{
			name: "per-access backend also collects the password",
			auth: true,
			wantFields: []providers.FormField{
				{Key: "name", Label: "Name", Placeholder: "github", Required: true},
				{Key: "secret", Label: "Secret", Placeholder: "base32 seed or otpauth:// URI", Required: true, Secret: true},
				{Key: "credential", Label: "Password", Required: true, Secret: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := stockedStore()
			store.auth = tt.auth
			p := newProvider(t, twoEntryIndex(), store, substringScorer)
			res, err := p.Query(context.Background(), "totp")
			if err != nil {
				t.Fatalf("Query() error: %v", err)
			}
			row := res[len(res)-1]
			if row.ID != "totp:add" || row.Score != AddScore {
				t.Fatalf("last row = %q score %d, want totp:add at %d", row.ID, row.Score, AddScore)
			}
			if row.Icon.ThemeName != iconAdd || row.Category != providers.CatTOTP {
				t.Errorf("icon/category = %q/%d, want %q/%d", row.Icon.ThemeName, row.Category, iconAdd, providers.CatTOTP)
			}
			if row.Form == nil {
				t.Fatal("Form = nil, want the add form")
			}
			if !reflect.DeepEqual(row.Form.Fields, tt.wantFields) {
				t.Errorf("Form.Fields = %+v, want %+v", row.Form.Fields, tt.wantFields)
			}
			if _, err := row.Form.Build(map[string]string{"name": "x"}); err == nil {
				t.Error("Build with a missing secret succeeded, want an error")
			}
			values := map[string]string{"name": "x", "secret": testSeed, "credential": "pw"}
			got, err := row.Form.Build(values)
			if err != nil {
				t.Fatalf("Build error: %v", err)
			}
			if got.Kind != ActTOTPAdd || !reflect.DeepEqual(got.Values, values) {
				t.Errorf("Build() = %+v, want kind %q carrying the submitted values", got, ActTOTPAdd)
			}
		})
	}
}

// keyringIndex is twoEntryIndex under the keyring backend, so a wizard
// recorded for "keyring" matches the configured backend.
func keyringIndex() Index {
	idx := twoEntryIndex()
	idx.Backend = secrets.BackendKeyring
	return idx
}

// failedState is a SetupState already carrying a failure for backend.
func failedState(backend, msg string) *SetupState {
	s := &SetupState{}
	s.Fail(backend, errors.New(msg))
	return s
}

// TestQueryWizard covers the gate in Query that swaps the ordinary rows for the
// setup wizard: it must fire only under the trigger, only for the backend the
// user would actually be using, and only while a failure is recorded — and it
// must offer the escape row exactly when no backend has been persisted yet.
func TestQueryWizard(t *testing.T) {
	const msg = "keyring probe failed: no session bus"
	wizardRows := []string{
		"totp:wizard:status",
		"totp:wizard:keyring:daemon",
		"totp:wizard:keyring:install",
		"totp:wizard:keyring:dbus",
		"totp:wizard:retry",
	}
	tests := []struct {
		name    string
		idx     Index
		state   *SetupState
		query   string
		wantIDs []string
		// wantMessage, when set, is asserted against the first row's subtitle:
		// the recorded error must reach the status row verbatim.
		wantMessage string
	}{
		{
			name:        "first-time setup gets the wizard with an escape row",
			idx:         Index{V: IndexVersion},
			state:       failedState(secrets.BackendKeyring, msg),
			query:       "totp",
			wantIDs:     append(append([]string(nil), wizardRows...), "totp:wizard:back"),
			wantMessage: msg,
		},
		{
			name:        "configured backend gets the wizard with no escape row",
			idx:         keyringIndex(),
			state:       failedState(secrets.BackendKeyring, msg),
			query:       "totp",
			wantIDs:     wizardRows,
			wantMessage: msg,
		},
		{
			name:    "untriggered query never sees the wizard",
			idx:     keyringIndex(),
			state:   failedState(secrets.BackendKeyring, msg),
			query:   "github",
			wantIDs: []string{"totp:github"},
		},
		{
			name:    "failure for another backend leaves the rows alone",
			idx:     twoEntryIndex(), // plaintext
			state:   failedState(secrets.BackendKeyring, msg),
			query:   "totp",
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add"},
		},
		{
			name:    "cleared state leaves the rows alone",
			idx:     keyringIndex(),
			state:   &SetupState{},
			query:   "totp",
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add"},
		},
		{
			name:    "no state wired up leaves setup rows alone",
			idx:     Index{V: IndexVersion},
			state:   nil,
			query:   "totp",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := stockedStore()
			p := New(substringScorer,
				WithIndexPath(writeIndex(t, tt.idx)),
				WithOpenStore(func(string) (secrets.Store, error) { return store, nil }),
				WithNow(fixedNow),
				WithSetupState(tt.state),
			)
			res, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query(%q) error: %v", tt.query, err)
			}
			if got := ids(res); !reflect.DeepEqual(got, tt.wantIDs) {
				t.Errorf("Query(%q) ids = %v, want %v", tt.query, got, tt.wantIDs)
			}
			if tt.wantMessage != "" && res[0].Subtitle != tt.wantMessage {
				t.Errorf("status subtitle = %q, want the recorded error %q", res[0].Subtitle, tt.wantMessage)
			}
		})
	}
}

// TestQueryWizardClearedMidLife proves the wizard is not sticky: clearing the
// shared state is all it takes for the very next query to render normally,
// which is what makes a successful retry look like the wizard never happened.
func TestQueryWizardClearedMidLife(t *testing.T) {
	state := failedState(secrets.BackendKeyring, "boom")
	p := New(substringScorer,
		WithIndexPath(writeIndex(t, keyringIndex())),
		WithOpenStore(func(string) (secrets.Store, error) { return stockedStore(), nil }),
		WithNow(fixedNow),
		WithSetupState(state),
	)
	res, err := p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	if got := ids(res); len(got) == 0 || got[0] != "totp:wizard:status" {
		t.Fatalf("Query() ids = %v, want the wizard first", got)
	}
	state.Clear()
	res, err = p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() after Clear error: %v", err)
	}
	want := []string{"totp:github", "totp:gitlab", "totp:add"}
	if got := ids(res); !reflect.DeepEqual(got, want) {
		t.Errorf("Query() after Clear ids = %v, want %v", got, want)
	}
}

func TestQueryStoreFailureDegradesRow(t *testing.T) {
	tests := []struct {
		name         string
		getErr       error
		stored       string
		wantSubtitle string
	}{
		{name: "missing key", getErr: secrets.ErrNotFound, wantSubtitle: "no secret stored for this entry"},
		{name: "locked backend", getErr: secrets.ErrAuthRequired, wantSubtitle: "locked — authentication required"},
		{name: "broken backend", getErr: secrets.ErrNotConfigured, wantSubtitle: "secrets backend unavailable"},
		{name: "unclassified failure", getErr: errors.New("boom"), wantSubtitle: "code unavailable"},
		{name: "corrupt stored seed", stored: "!!!!", wantSubtitle: "stored secret is not valid base32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore("plaintext")
			store.getErr = tt.getErr
			if tt.stored != "" {
				store.values["totp/github"] = tt.stored
			}
			idx := Index{V: IndexVersion, Backend: "plaintext", Entries: []Entry{{Name: "github"}}}
			p := newProvider(t, idx, store, substringScorer)
			res, err := p.Query(context.Background(), "totp")
			if err != nil {
				t.Fatalf("Query() error: %v", err)
			}
			if len(res) != 2 {
				t.Fatalf("Query() rows = %d, want the entry plus the add row", len(res))
			}
			if res[0].Subtitle != tt.wantSubtitle {
				t.Errorf("Subtitle = %q, want %q", res[0].Subtitle, tt.wantSubtitle)
			}
			if !res[0].Expiry.IsZero() {
				t.Errorf("Expiry = %v, want zero (nothing is counting down)", res[0].Expiry)
			}
			if res[0].Action.Kind != ActTOTPCopy {
				t.Errorf("Action.Kind = %q, want %q (activation may still succeed)", res[0].Action.Kind, ActTOTPCopy)
			}
		})
	}
}

// hangingStore is a Store whose Get never answers on its own, standing in for a
// Secret Service parked on an unlock prompt.
type hangingStore struct{ fakeStore }

func (h *hangingStore) Get(ctx context.Context, _ string, _ secrets.Credential) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestQueryBoundsAWedgedBackend is the aggregator's protection: the launcher's
// query context has no deadline, so without the provider's own bound a backend
// sitting on an unlock prompt would hold the aggregator's Wait — and therefore
// every other provider's results — until the user answered. The row must
// degrade instead, and the query must still return.
func TestQueryBoundsAWedgedBackend(t *testing.T) {
	store := &hangingStore{fakeStore{name: "keyring", blocking: true, values: map[string]string{}}}
	idx := Index{V: IndexVersion, Backend: "keyring", Entries: []Entry{{Name: "github"}}}
	p := New(substringScorer,
		WithIndexPath(writeIndex(t, idx)),
		WithOpenStore(func(string) (secrets.Store, error) { return store, nil }),
		WithNow(fixedNow),
		WithQueryTimeout(20*time.Millisecond),
	)

	res, err := p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() error = %v, want the wedged read to degrade one row", err)
	}
	if len(res) != 2 {
		t.Fatalf("Query() rows = %d, want the entry plus the add row", len(res))
	}
	if res[0].Subtitle != "code unavailable" {
		t.Errorf("Subtitle = %q, want %q", res[0].Subtitle, "code unavailable")
	}
	if !res[0].Expiry.IsZero() {
		t.Errorf("Expiry = %v, want zero (nothing is counting down)", res[0].Expiry)
	}
}

func TestQueryHonorsContext(t *testing.T) {
	p := newProvider(t, twoEntryIndex(), stockedStore(), substringScorer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Query(ctx, "totp"); !errors.Is(err, context.Canceled) {
		t.Errorf("Query() error = %v, want context.Canceled", err)
	}
}

func TestQueryBrokenIndexIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	p := New(substringScorer,
		WithIndexPath(path),
		WithOpenStore(func(string) (secrets.Store, error) { return newFakeStore("plaintext"), nil }),
		WithNow(fixedNow),
	)
	if _, err := p.Query(context.Background(), "totp"); err == nil {
		t.Error("Query() on a corrupt index succeeded, want an error the aggregator can log")
	}
}

func TestProviderName(t *testing.T) {
	if got := New(substringScorer).Name(); got != "totp" {
		t.Errorf("Name() = %q, want %q", got, "totp")
	}
}
