package totp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// multiIndex is the two-manager fixture: keyring is the default (the legacy
// Backend field, per the AddBackend invariant), plaintext is the second, and
// "gitlab" lives in the second one while "github" takes the default.
func multiIndex() Index {
	return Index{
		V:        IndexVersion,
		Backend:  "keyring",
		Backends: []string{"plaintext"},
		Entries: []Entry{
			{Name: "github"},
			{Name: "gitlab", Backend: "plaintext"},
		},
	}
}

// storeSwitch builds a WithOpenStore that resolves each backend name to its own
// fake vault, which is what lets a test prove *which* manager a row was rendered
// from. An unknown name fails the open, standing in for a manager banshee cannot
// construct.
func storeSwitch(stores map[string]*fakeStore) func(string) (secrets.Store, error) {
	return func(name string) (secrets.Store, error) {
		s, ok := stores[name]
		if !ok {
			return nil, fmt.Errorf("no store for %q", name)
		}
		return s, nil
	}
}

// rowByID finds one result by ID, so a test asserting on the add row does not
// have to know how many rows follow it.
func rowByID(res []providers.Result, id string) (providers.Result, bool) {
	for _, r := range res {
		if r.ID == id {
			return r, true
		}
	}
	return providers.Result{}, false
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
			name:    "bare trigger lists everything with add and the storage hint last",
			query:   "totp",
			scorer:  substringScorer,
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add", "totp:setup:more"},
		},
		{
			name:    "trigger remainder filters",
			query:   "totp lab",
			scorer:  substringScorer,
			wantIDs: []string{"totp:gitlab", "totp:add", "totp:setup:more"},
		},
		{
			name:    "trigger remainder matching nothing keeps add row",
			query:   "otp nothing",
			scorer:  substringScorer,
			wantIDs: []string{"totp:add", "totp:setup:more"},
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
		// Period rides with Expiry: the UI drains a bar over the window ending
		// there and cannot know how long that window was without being told.
		Period: 30 * time.Second,
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

// nameField and secretField are the two fields every add form starts with, so
// the table below only spells out what the manager configuration adds.
func nameField() providers.FormField {
	return providers.FormField{Key: "name", Label: "Name", Placeholder: "github", Required: true}
}

func secretField() providers.FormField {
	return providers.FormField{Key: "secret", Label: "Secret", Placeholder: "base32 seed or otpauth:// URI", Required: true, Secret: true}
}

// TestQueryAddRowForm pins the add form against every manager configuration,
// which is where the two rules that depend on it live: the Storage dropdown
// (present only when there is a choice to make, first option = the index's
// default) and the credential field (required for a single per-access manager,
// present-but-optional as soon as the manager is a dropdown choice, because
// which one needs a password is not known until the user picks).
func TestQueryAddRowForm(t *testing.T) {
	tests := []struct {
		name string
		idx  Index
		// auth names the backends whose fake store authenticates per access.
		auth       []string
		wantFields []providers.FormField
	}{
		{
			name:       "single local manager",
			idx:        twoEntryIndex(),
			wantFields: []providers.FormField{nameField(), secretField()},
		},
		{
			name: "single per-access manager requires the password",
			idx:  twoEntryIndex(),
			auth: []string{"plaintext"},
			wantFields: []providers.FormField{
				nameField(), secretField(),
				{Key: "credential", Label: "Password", Required: true, Secret: true},
			},
		},
		{
			name: "two local managers get a dropdown and no password",
			idx:  multiIndex(),
			wantFields: []providers.FormField{
				nameField(), secretField(),
				{Key: "backend", Label: "Storage", Options: []string{"keyring", "plaintext"}},
			},
		},
		{
			name: "one per-access manager among several makes the password optional",
			idx:  multiIndex(),
			auth: []string{"plaintext"},
			wantFields: []providers.FormField{
				nameField(), secretField(),
				{Key: "backend", Label: "Storage", Options: []string{"keyring", "plaintext"}},
				{Key: "credential", Label: "Password (plaintext only)", Secret: true},
			},
		},
		{
			name: "every per-access manager is named in the label",
			idx:  multiIndex(),
			auth: []string{"keyring", "plaintext"},
			wantFields: []providers.FormField{
				nameField(), secretField(),
				{Key: "backend", Label: "Storage", Options: []string{"keyring", "plaintext"}},
				{Key: "credential", Label: "Password (keyring, plaintext only)", Secret: true},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stores := map[string]*fakeStore{}
			for _, name := range tt.idx.Configured() {
				s := newFakeStore(name)
				s.auth = slices.Contains(tt.auth, name)
				stores[name] = s
			}
			p := New(substringScorer,
				WithIndexPath(writeIndex(t, tt.idx)),
				WithOpenStore(storeSwitch(stores)),
				WithNow(fixedNow),
			)
			res, err := p.Query(context.Background(), "totp")
			if err != nil {
				t.Fatalf("Query() error: %v", err)
			}
			row, ok := rowByID(res, "totp:add")
			if !ok {
				t.Fatalf("no add row in %v", ids(res))
			}
			if row.Score != AddScore {
				t.Fatalf("add row scores %d, want %d", row.Score, AddScore)
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

// TestQueryMultiBackend is the routing contract with several managers
// configured: each entry's code is read from the manager that holds it, each
// manager's own AuthPerAccess answer shapes only its own rows, and every
// subtitle says which vault it came from — a code with no attribution is
// useless when two of them could have answered.
func TestQueryMultiBackend(t *testing.T) {
	keyring := newFakeStore("keyring")
	keyring.values["totp/github"] = testSeed
	plaintext := newFakeStore("plaintext")
	plaintext.values["totp/gitlab"] = testSeed

	p := New(substringScorer,
		WithIndexPath(writeIndex(t, multiIndex())),
		WithOpenStore(storeSwitch(map[string]*fakeStore{"keyring": keyring, "plaintext": plaintext})),
		WithNow(fixedNow),
	)
	res, err := p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() error: %v", err)
	}
	wantSubtitles := map[string]string{
		"totp:github": "324 550 · keyring",
		"totp:gitlab": "324 550 · plaintext",
	}
	for id, want := range wantSubtitles {
		row, ok := rowByID(res, id)
		if !ok {
			t.Fatalf("no row %q in %v", id, ids(res))
		}
		if row.Subtitle != want {
			t.Errorf("%s subtitle = %q, want %q", id, row.Subtitle, want)
		}
	}
	// Each vault answered for its own entry and nothing else: a second read
	// against either would mean the provider asked the wrong manager first.
	if len(keyring.creds) != 1 || len(plaintext.creds) != 1 {
		t.Errorf("reads = keyring %d, plaintext %d, want exactly one each", len(keyring.creds), len(plaintext.creds))
	}

	t.Run("per-manager auth shapes only its own rows", func(t *testing.T) {
		locked := newFakeStore("plaintext")
		locked.auth = true
		open := newFakeStore("keyring")
		open.values["totp/github"] = testSeed
		p := New(substringScorer,
			WithIndexPath(writeIndex(t, multiIndex())),
			WithOpenStore(storeSwitch(map[string]*fakeStore{"keyring": open, "plaintext": locked})),
			WithNow(fixedNow),
		)
		res, err := p.Query(context.Background(), "totp")
		if err != nil {
			t.Fatalf("Query() error: %v", err)
		}
		github, _ := rowByID(res, "totp:github")
		gitlab, _ := rowByID(res, "totp:gitlab")
		if github.Subtitle != "324 550 · keyring" || github.Form != nil {
			t.Errorf("github row = %q form %v, want the code from the non-auth manager", github.Subtitle, github.Form != nil)
		}
		if gitlab.Subtitle != "Enter to unlock and copy · plaintext" || gitlab.Form == nil {
			t.Errorf("gitlab row = %q form %v, want the unlock prompt from the auth manager", gitlab.Subtitle, gitlab.Form != nil)
		}
		if len(locked.creds) != 0 {
			t.Errorf("the per-access manager was read %d times while rendering, want 0", len(locked.creds))
		}
	})

	t.Run("a single manager labels nothing", func(t *testing.T) {
		p := newProvider(t, twoEntryIndex(), stockedStore(), substringScorer)
		res, err := p.Query(context.Background(), "totp")
		if err != nil {
			t.Fatalf("Query() error: %v", err)
		}
		row, _ := rowByID(res, "totp:github")
		if row.Subtitle != "324 550" {
			t.Errorf("subtitle = %q, want the bare code: one manager's name is on every row and says nothing", row.Subtitle)
		}
	})
}

// TestQueryPeriodFollowsEntry pins Period alongside Expiry: the UI drains a bar
// over the window ending at Expiry, and a 60-second entry that reported the
// standard 30 would drain twice as fast as its code actually rotates.
func TestQueryPeriodFollowsEntry(t *testing.T) {
	tests := []struct {
		name   string
		period int
		want   time.Duration
	}{
		{name: "default period", period: 0, want: 30 * time.Second},
		{name: "explicit 30s", period: 30, want: 30 * time.Second},
		{name: "60s", period: 60, want: 60 * time.Second},
		{name: "15s", period: 15, want: 15 * time.Second},
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
			if res[0].Period != tt.want {
				t.Errorf("Period = %v, want %v", res[0].Period, tt.want)
			}
			if res[0].Expiry.IsZero() {
				t.Error("Expiry is zero, so Period describes a window nothing counts down to")
			}
		})
	}
}

// TestQuerySetupToken covers "totp setup": the chooser has to be reachable from
// a typed query, because the provider keeps no state between queries and there
// is no settings surface. It offers only the managers not configured yet, and
// once they are all configured it stops shadowing the ordinary listing.
func TestQuerySetupToken(t *testing.T) {
	allConfigured := Index{
		V:        IndexVersion,
		Backend:  "keyring",
		Backends: []string{"plaintext", "nimbus"},
		Entries:  []Entry{{Name: "github"}},
	}
	tests := []struct {
		name    string
		idx     Index
		query   string
		wantIDs []string
	}{
		{
			name:    "one manager configured offers the other two",
			idx:     twoEntryIndex(), // plaintext
			query:   "totp setup",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:nimbus"},
		},
		{
			name:    "the token is case-insensitive",
			idx:     twoEntryIndex(),
			query:   "otp SETUP",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:nimbus"},
		},
		{
			name:    "two configured leave one",
			idx:     multiIndex(),
			query:   "totp setup",
			wantIDs: []string{"totp:setup:nimbus"},
		},
		{
			name:  "all configured falls through to the listing",
			idx:   allConfigured,
			query: "totp setup",
			// No hint row: there is nothing left to add. The entry misses the
			// "setup" filter, so only the add row survives.
			wantIDs: []string{"totp:add"},
		},
		{
			name:    "nothing configured ignores the token and offers everything",
			idx:     Index{V: IndexVersion},
			query:   "totp setup",
			wantIDs: []string{"totp:setup:keyring", "totp:setup:plaintext", "totp:setup:nimbus"},
		},
		{
			name:    "the token only works under the trigger",
			idx:     twoEntryIndex(),
			query:   "setup",
			wantIDs: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(t, tt.idx, stockedStore(), substringScorer)
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

// TestQuerySetupMoreRow pins the hint row that leads to the chooser: it appears
// only under the trigger, only while something is left to configure, and it
// scores below the add row so it is the last thing in the block.
func TestQuerySetupMoreRow(t *testing.T) {
	tests := []struct {
		name  string
		idx   Index
		query string
		want  bool
	}{
		{name: "one manager configured", idx: twoEntryIndex(), query: "totp", want: true},
		{
			// Both managers that can actually be configured today. Counting the
			// chooser's Nimbus row here instead would keep the hint on screen
			// forever, leading only to a row the setup handler refuses.
			name:  "every configurable manager configured",
			idx:   multiIndex(),
			query: "totp",
			want:  false,
		},
		{
			name: "a hand-written nimbus entry does not resurrect it",
			idx: Index{V: IndexVersion, Backend: "keyring", Backends: []string{"plaintext", "nimbus"},
				Entries: []Entry{{Name: "github"}}},
			query: "totp",
			want:  false,
		},
		{name: "nothing configured shows the chooser instead", idx: Index{V: IndexVersion}, query: "totp", want: false},
		{name: "untriggered queries never carry it", idx: twoEntryIndex(), query: "github", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider(t, tt.idx, stockedStore(), substringScorer)
			res, err := p.Query(context.Background(), tt.query)
			if err != nil {
				t.Fatalf("Query(%q) error: %v", tt.query, err)
			}
			row, ok := rowByID(res, "totp:setup:more")
			if ok != tt.want {
				t.Fatalf("hint row present = %v, want %v (ids %v)", ok, tt.want, ids(res))
			}
			if !ok {
				return
			}
			if row.Score != SetupMoreScore || row.Score >= AddScore {
				t.Errorf("hint row scores %d, want %d and below AddScore %d", row.Score, SetupMoreScore, AddScore)
			}
			if row.Action.Kind != ActTOTPSetupMore || row.Category != providers.CatTOTP {
				t.Errorf("hint row action/category = %q/%d, want %q/CatTOTP", row.Action.Kind, row.Category, ActTOTPSetupMore)
			}
			if res[len(res)-1].ID != "totp:setup:more" {
				t.Errorf("last row = %q, want the hint row emitted last", res[len(res)-1].ID)
			}
		})
	}
}

// TestQueryUnopenableEntryBackendDegradesRow is the multi-manager blast radius:
// one manager that will not open must cost exactly its own rows. Failing the
// whole query instead would blank the codes held in every other vault, which is
// the opposite of what configuring a second one is for.
func TestQueryUnopenableEntryBackendDegradesRow(t *testing.T) {
	keyring := newFakeStore("keyring")
	keyring.values["totp/github"] = testSeed
	// plaintext is missing from the map, so opening it fails.
	p := New(substringScorer,
		WithIndexPath(writeIndex(t, multiIndex())),
		WithOpenStore(storeSwitch(map[string]*fakeStore{"keyring": keyring})),
		WithNow(fixedNow),
	)
	res, err := p.Query(context.Background(), "totp")
	if err != nil {
		t.Fatalf("Query() error = %v, want the unopenable manager to degrade its own row", err)
	}
	github, _ := rowByID(res, "totp:github")
	if github.Subtitle != "324 550 · keyring" {
		t.Errorf("github subtitle = %q, want the working manager's code", github.Subtitle)
	}
	gitlab, ok := rowByID(res, "totp:gitlab")
	if !ok {
		t.Fatalf("the entry in the broken manager vanished from %v", ids(res))
	}
	if gitlab.Subtitle != "secrets backend unavailable · plaintext" {
		t.Errorf("gitlab subtitle = %q, want the failure named against its manager", gitlab.Subtitle)
	}
	if !gitlab.Expiry.IsZero() {
		t.Errorf("Expiry = %v, want zero (nothing is counting down)", gitlab.Expiry)
	}
	if gitlab.Action.Kind != ActTOTPCopy {
		t.Errorf("Action.Kind = %q, want %q (activation may still explain itself)", gitlab.Action.Kind, ActTOTPCopy)
	}
}

// keyringIndex is twoEntryIndex under the keyring backend, so a wizard
// recorded for "keyring" matches the configured backend.
func keyringIndex() Index {
	idx := twoEntryIndex()
	idx.Backend = secrets.BackendKeyring
	return idx
}

// failedState is a SetupState already carrying a failure for backend, raised by
// ordinary use of it.
func failedState(backend, msg string) *SetupState {
	s := &SetupState{}
	s.Fail(backend, errors.New(msg))
	return s
}

// failedSetupState is failedState for a failure raised by an explicit attempt to
// configure backend, which the gate renders even for a manager the index does
// not list.
func failedSetupState(backend, msg string) *SetupState {
	s := &SetupState{}
	s.FailSetup(backend, errors.New(msg))
	return s
}

// TestQueryWizard covers the gate in Query that swaps the ordinary rows for the
// setup wizard: it must fire only under the trigger, only for the backend the
// user would actually be using, and only while a failure is recorded — and it
// must offer the escape row exactly when the failing manager is not one the user
// already keeps seeds in (nothing configured, or one being added right now).
func TestQueryWizard(t *testing.T) {
	const msg = "keyring probe failed: no session bus"
	wizardRows := []string{
		"totp:wizard:status",
		"totp:wizard:keyring:daemon",
		"totp:wizard:keyring:create",
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
			// Adding a second manager means probing one that is deliberately not
			// configured yet, so the "is it still configured" guard would discard
			// exactly the diagnosis the user is waiting for. The setup flag is what
			// keeps that report on screen — and the escape row comes with it, because
			// the manager that failed is not one the user depends on: without it the
			// trigger would render this wizard for the daemon's lifetime and the codes
			// already working in the first manager would be unreachable from it.
			name:        "a failed attempt to add a manager opens the wizard with an escape row",
			idx:         twoEntryIndex(), // plaintext configured, keyring being added
			state:       failedSetupState(secrets.BackendKeyring, msg),
			query:       "totp",
			wantIDs:     append(append([]string(nil), wizardRows...), "totp:wizard:back"),
			wantMessage: msg,
		},
		{
			name:    "failure for another backend leaves the rows alone",
			idx:     twoEntryIndex(), // plaintext
			state:   failedState(secrets.BackendKeyring, msg),
			query:   "totp",
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add", "totp:setup:more"},
		},
		{
			name:    "cleared state leaves the rows alone",
			idx:     keyringIndex(),
			state:   &SetupState{},
			query:   "totp",
			wantIDs: []string{"totp:github", "totp:gitlab", "totp:add", "totp:setup:more"},
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
				// Without this the wizard's install row would probe the real
				// PATH and render differently on a machine with pacman than in a
				// bare container.
				WithLookPath(noPackageManager),
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
		WithLookPath(noPackageManager),
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
	want := []string{"totp:github", "totp:gitlab", "totp:add", "totp:setup:more"}
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
			if len(res) != 3 {
				t.Fatalf("Query() rows = %d, want the entry plus the add and storage-hint rows", len(res))
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
	if len(res) != 3 {
		t.Fatalf("Query() rows = %d, want the entry plus the add and storage-hint rows", len(res))
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
