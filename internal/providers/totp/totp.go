package totp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/jourdanhaines/banshee/internal/config"
	"github.com/jourdanhaines/banshee/internal/providers"
	"github.com/jourdanhaines/banshee/internal/secrets"
)

// Scorer scores a candidate string against a query, reporting whether it
// matched at all. It mirrors the signature of internal/fuzzy.Score; the
// concrete implementation is injected so this package stays independent of the
// ranking implementation (same seam as procs.Scorer).
type Scorer func(query, candidate string) (int, bool)

// Scores this provider emits. They are constants rather than literals because
// the three row kinds must keep a fixed relative order no matter what the
// fuzzy scorer returns for an entry name.
const (
	// TriggerScore is what an entry (or a setup row) gets when the user typed
	// the bare trigger. It sits below calc's 1000 so an arithmetic query still
	// answers first, and far above any organic fuzzy score so the full list
	// stays together at the top while the trigger is on screen.
	TriggerScore = 800
	// AddScore keeps the "Add TOTP code" row pinned last within the triggered
	// block: every real entry outranks it, but it never disappears.
	AddScore = 10
	// SetupMoreScore puts the "add another secrets manager" hint under the add
	// row: configuring storage is rarer than adding a code, so it is the last
	// thing in the triggered block and the first thing pushed off a full screen.
	SetupMoreScore = 5
)

// setupToken is the word after the trigger that reopens the manager chooser
// ("totp setup"). It is a query rather than a mode or a flag because the
// provider holds no state between queries: the chooser has to be reachable from
// a freshly typed query alone, which is also what lets the hint row simply
// reopen the launcher on reopenSetupQuery.
const setupToken = "setup"

// reopenSetupQuery is what the launcher is reopened with to land on the manager
// chooser: the trigger plus setupToken, spelled once so the hint row's handler
// and the query parser cannot disagree.
const reopenSetupQuery = "totp " + setupToken

// configurableBackends is every secrets manager a user can actually configure
// today. It is what decides whether the "add another secrets manager" hint is
// worth showing: with all of them configured there is nothing left to choose,
// and offering the hint anyway would walk the user to a chooser whose only
// remaining row (Nimbus) the setup handler refuses outright.
//
// The chooser itself still lists Nimbus — a "coming soon" row is the only thing
// that ever tells the user a cloud backend is planned — which is exactly why the
// two lists are separate rather than one.
var configurableBackends = []string{secrets.BackendKeyring, secrets.BackendPlaintext}

// moreBackendsConfigurable reports whether any manager the user could configure
// today is still missing from configured.
func moreBackendsConfigurable(configured []string) bool {
	for _, name := range configurableBackends {
		if !slices.Contains(configured, name) {
			return true
		}
	}
	return false
}

// queryTimeout bounds one entry's backend read while rendering a query.
//
// The launcher's query context has no deadline of its own — it is cancelled by
// the next keystroke and by nothing else — so a Blocking backend sitting on an
// unlock prompt would otherwise stall the aggregator's errgroup Wait for as
// long as the user took to answer, withholding every other provider's results
// and leaving the list empty. One wedged backend must degrade to a "code
// unavailable" subtitle instead. It is far longer than any healthy read (a
// local file, or an unlocked Secret Service round trip) needs.
const queryTimeout = 2 * time.Second

// Icon theme names used by this provider's rows.
const (
	iconEntry = "security-high-symbolic"
	iconAdd   = "list-add-symbolic"
)

// Option configures a Provider.
type Option func(*Provider)

// WithIndexPath overrides the totp.json location. Tests point it at a
// t.TempDir() file so the provider never reads the developer's real index.
func WithIndexPath(path string) Option {
	return func(p *Provider) {
		if path != "" {
			p.indexPath = path
		}
	}
}

// WithOpenStore overrides how a backend name becomes a secrets.Store. It
// exists so tests can inject a fake vault (and so a future caller can pin a
// pre-configured Nimbus client) without this package reaching for real
// storage.
func WithOpenStore(open func(name string) (secrets.Store, error)) Option {
	return func(p *Provider) {
		if open != nil {
			p.open = open
		}
	}
}

// WithNow overrides the clock. Codes and expiry instants are derived from it,
// so a fixed clock makes every row in a test byte-for-byte predictable.
func WithNow(now func() time.Time) Option {
	return func(p *Provider) {
		if now != nil {
			p.now = now
		}
	}
}

// WithQueryTimeout overrides how long one entry's backend read may take before
// its row degrades to an "unavailable" subtitle. It exists so a test can prove
// that degradation without waiting out the real bound; nothing in production
// passes it.
func WithQueryTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.queryTimeout = d
		}
	}
}

// WithLookPath overrides how the wizard probes PATH for a program. The setup
// wizard uses it to decide whether it can offer a real package-manager install
// command, so a test injects a fake to pin those rows on any machine —
// production wants the real exec.LookPath.
func WithLookPath(lookPath func(name string) (string, error)) Option {
	return func(p *Provider) {
		if lookPath != nil {
			p.lookPath = lookPath
		}
	}
}

// Provider turns the TOTP index into launcher rows showing live codes.
//
// It holds no state between queries: the index is a small local file and the
// secrets backend is opened per query, so an entry added by the add handler
// (or a backend chosen by the setup handler) shows up on the very next
// keystroke without a reload.
type Provider struct {
	score        Scorer
	indexPath    string
	open         func(name string) (secrets.Store, error)
	now          func() time.Time
	queryTimeout time.Duration
	// setup is the shared backend-failure state, injected by WithSetupState.
	// Nil means no wizard: the provider renders its ordinary rows.
	setup *SetupState
	// lookPath probes PATH on the wizard's behalf; see WithLookPath.
	lookPath func(name string) (string, error)
}

var _ providers.Provider = (*Provider)(nil)

// New returns the TOTP provider scoring entry names with score. Defaults:
// the real index path, secrets.Open, time.Now and exec.LookPath — all
// overridable by Option.
func New(score Scorer, opts ...Option) *Provider {
	p := &Provider{
		score:        score,
		indexPath:    config.TOTPIndexPath(),
		open:         secrets.Open,
		now:          time.Now,
		queryTimeout: queryTimeout,
		lookPath:     exec.LookPath,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Name implements providers.Provider.
func (p *Provider) Name() string { return "totp" }

// Query renders the TOTP rows for q.
//
// Behavior, in the order the checks run:
//
//   - An empty query returns nothing. Codes are secret material and the
//     launcher's idle state is visible to anyone glancing at the screen.
//   - "totp" / "otp" (optionally followed by a filter) is the trigger: it
//     lists every entry, plus the "Add TOTP code" row and — while a manager the
//     user could actually configure is still missing — the "add another secrets
//     manager" hint, last.
//   - Without the trigger, entry names are fuzzy-matched and only positive
//     matches appear, so typing a repo name never scatters codes into the list.
//   - With no manager configured yet, the trigger offers the setup rows
//     instead of entries; without the trigger it stays silent.
//   - "totp setup" reopens the chooser for the managers not configured yet, so
//     a second one can be added without a settings screen. With all of them
//     configured it falls through to the ordinary listing.
//   - While a backend failure is recorded in the shared SetupState, the trigger
//     renders the setup wizard instead of any of the above.
//
// A manager that cannot even be opened degrades the rows that live in it to an
// explanatory subtitle rather than failing the whole query: with several
// managers configured, one broken vault must not blank the codes held in the
// others.
//
// # Shared-score contract
//
// TOTP rows are not repo-derived, so they deliberately sit outside every repo
// block — the same exception the connectors provider's link row takes: they
// score the entry name, which is a user-chosen label, not a repo basename.
// Because CatTOTP ranks above CatApp it is exempt from the aggregator's
// MinScore threshold, so this provider does its own thresholding: untriggered
// matches must score above zero to appear at all.
func (p *Provider) Query(ctx context.Context, q string) ([]providers.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	triggered, filter := parseTrigger(q)

	// Reload every query: the file is a few hundred bytes and the add/setup
	// handlers write it behind the provider's back.
	idx, err := LoadIndex(p.indexPath)
	if err != nil {
		return nil, err
	}
	configured := idx.Configured()
	// A recorded backend failure replaces every other row, but only under the
	// trigger: an untriggered query that merely fuzzy-matches an entry name must
	// never leak a backend diagnosis into a list the user did not ask TOTP for.
	// The configured guard drops a stale wizard — a failure recorded against a
	// manager the user has since reconfigured out of band no longer describes
	// anything they would be using, so the ordinary rows win. The exception is a
	// failure raised by an explicit setup attempt (fromSetup): that names a
	// manager the user is trying to add right now, which is by definition not
	// configured yet, and it is the one case where a not-configured manager's
	// diagnosis is exactly what they asked for. The escape row is offered for
	// exactly the wizards that name a manager the user does not depend on yet —
	// nothing configured at all, or one they were in the middle of adding — because
	// those are the ones there is somewhere safe to go back from; that is the
	// offerBack argument, and without it a failed attempt to add a second manager
	// would hold the trigger hostage for the daemon's lifetime, hiding the codes
	// already working in the first. Snapshot is nil-receiver-safe, so a provider
	// with no SetupState wired up needs no check of its own.
	if triggered {
		if backend, message, fromSetup, ok := p.setup.Snapshot(); ok && (fromSetup || wizardApplies(configured, backend)) {
			return wizardResults(backend, message, len(configured) == 0 || fromSetup, p.lookPath), nil
		}
	}
	if len(configured) == 0 {
		if !triggered {
			return nil, nil
		}
		return p.setupResults(nil), nil
	}
	// The chooser token, for adding a manager to the ones already configured.
	// It deliberately shadows the fuzzy filter: an entry literally named "setup"
	// stops being reachable by "totp setup" while any manager is unconfigured.
	// That is accepted — the entry is still reachable by its bare name and by
	// any other filter, while the alternative is a settings surface banshee does
	// not have.
	if triggered && strings.EqualFold(filter, setupToken) {
		if rows := p.setupResults(configured); len(rows) > 0 {
			return rows, nil
		}
	}

	def := idx.DefaultBackend()
	// One open per distinct manager per query, cached: several entries usually
	// share a manager, and opening a store may construct a client. A failed open
	// is cached too, so a broken manager is diagnosed once rather than once per
	// row.
	opened := map[string]openedStore{}
	openFor := func(name string) openedStore {
		if s, ok := opened[name]; ok {
			return s
		}
		s := openedStore{}
		if store, err := p.open(name); err != nil {
			s.err = err
		} else {
			s.store = store
			s.auth = store.AuthPerAccess()
		}
		opened[name] = s
		return s
	}
	multi := len(configured) > 1
	now := p.now()

	var out []providers.Result
	for _, e := range idx.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		score, ok := p.matchScore(triggered, filter, q, e.Name)
		if !ok {
			continue
		}
		backend := e.BackendOr(def)
		row, err := p.entryResult(ctx, openFor(backend), e, score, now, backendLabel(backend, multi))
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if triggered {
		out = append(out, p.addResult(configured, func(name string) bool { return openFor(name).auth }))
		if moreBackendsConfigurable(configured) {
			out = append(out, setupMoreResult())
		}
	}
	return out, nil
}

// openedStore is one manager's opened Store, or the failure to open it, cached
// for the length of a query. auth is read once here rather than per row so a
// backend cannot answer differently halfway down the list.
type openedStore struct {
	store secrets.Store
	auth  bool
	err   error
}

// matchScore decides whether entry name belongs in the result list and at what
// score. The bare trigger admits everything at TriggerScore; anything else
// goes through the injected scorer and must come back strictly positive (see
// the thresholding note on Query).
func (p *Provider) matchScore(triggered bool, filter, q, name string) (int, bool) {
	needle := q
	if triggered {
		if filter == "" {
			return TriggerScore, true
		}
		needle = filter
	}
	if p.score == nil {
		return 0, false
	}
	s, ok := p.score(needle, name)
	if !ok || s <= 0 {
		return 0, false
	}
	return s, true
}

// entryResult builds one entry's row, reading the seed from the manager that
// holds it (st) and tagging the row with label when there is more than one
// manager to tell apart.
//
// For a local backend the code is computed here so the row shows it directly,
// and Expiry plus Period are set so the UI can drain a progress bar over the
// window and re-query when the code rotates. Period is set from the entry's own
// parameters even for the standard 30 seconds: the provider knows the window it
// derived Expiry from, and making the UI assume it would break the moment an
// entry is not standard. A backend that authenticates per access gets no code at
// all: asking for the password once per keystroke would be absurd, so the row
// carries a masked credential form and defers everything to activation.
//
// A backend failure for one entry degrades that row to an explanatory subtitle
// rather than failing the query — one unreadable key, or one manager that will
// not open, must not blank the list. That includes a read that ran past
// queryTimeout, which is why the abort check consults the caller's ctx rather
// than the bounded one: only a cancelled query aborts, because that means the
// user has already typed the next character.
func (p *Provider) entryResult(ctx context.Context, st openedStore, e Entry, score int, now time.Time, label string) (providers.Result, error) {
	params := e.Params()
	res := providers.Result{
		ID:       "totp:" + e.Name,
		Title:    e.Name,
		Icon:     providers.Icon{ThemeName: iconEntry},
		Category: providers.CatTOTP,
		Score:    score,
		Action:   providers.Action{Kind: ActTOTPCopy, Argv: []string{e.Name}},
		Period:   time.Duration(params.Period) * time.Second,
	}
	if st.err != nil {
		// The manager itself could not be opened — an unknown name in the index,
		// a client that refuses to construct. The row stays activatable: the copy
		// handler reports the same failure with the room to explain it.
		res.Subtitle = withLabel("secrets backend unavailable", label)
		return res, nil
	}
	if st.auth {
		name := e.Name
		res.Subtitle = withLabel("Enter to unlock and copy", label)
		res.Form = &providers.Form{
			Title:  "Unlock " + name,
			Fields: []providers.FormField{credentialField()},
			Build: func(values map[string]string) (providers.Action, error) {
				if strings.TrimSpace(values["credential"]) == "" {
					return providers.Action{}, errCredentialRequired
				}
				return providers.Action{Kind: ActTOTPCopy, Argv: []string{name}, Values: values}, nil
			},
		}
		return res, nil
	}

	getCtx, cancel := context.WithTimeout(ctx, p.queryTimeout)
	defer cancel()
	secret, err := st.store.Get(getCtx, secretKey(e.Name), secrets.Credential{})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return providers.Result{}, ctxErr
		}
		res.Subtitle = withLabel(unavailable(err), label)
		return res, nil
	}
	raw, err := DecodeSecret(secret)
	if err != nil {
		res.Subtitle = withLabel("stored secret is not valid base32", label)
		return res, nil
	}
	code, err := Code(raw, now, params)
	if err != nil {
		res.Subtitle = withLabel(unavailable(err), label)
		return res, nil
	}
	res.Subtitle = withLabel(FormatCode(code), label)
	res.Expiry = ExpiryOf(now, params.Period)
	return res, nil
}

// backendLabel is the manager name a row is tagged with, or "" while only one
// manager is configured — for the single-manager user, who is most users, the
// name is the same on every row and therefore pure noise.
func backendLabel(name string, multi bool) string {
	if !multi {
		return ""
	}
	return name
}

// withLabel appends a manager label to a subtitle. It is applied to every
// subtitle an entry row can carry, code and diagnosis alike: with several
// managers configured, "no secret stored for this entry" is only actionable once
// the user knows which vault was asked.
func withLabel(subtitle, label string) string {
	if label == "" {
		return subtitle
	}
	if subtitle == "" {
		return label
	}
	return subtitle + " · " + label
}

// addResult is the row that opens the "add a code" form. It is emitted only
// under the trigger — an unfiltered list of every launcher query would carry a
// row nobody asked for — and always last, at AddScore.
//
// configured is every manager the seed could go to and authFor reports whether
// one of them authenticates per access. Two fields depend on them:
//
//   - With more than one manager configured the form grows a "Storage"
//     dropdown listing them in Configured() order, so the first option — the
//     one a dropdown preselects — is the index's default. A single manager gets
//     no dropdown: a one-option choice is a decision the user cannot make wrong
//     and does not want to be asked about.
//   - The masked credential field is required exactly when there is one manager
//     and it authenticates per access, which is the pre-multi-manager rule. With
//     several, the field appears whenever *any* of them authenticates but is not
//     required, because whether a password is needed depends on the dropdown
//     choice the user has not made yet. Enforcement stays where it always
//     effectively was: the store answers ErrAuthRequired at dispatch and the
//     handler turns that into a notification.
//
// Build only checks that the fields are non-empty. Everything else (base32
// validity, otpauth parsing, duplicate names, whether the chosen manager is
// configured) is the handler's job, because a mistyped seed is worth explaining
// after the fact rather than blocking submission on a parse the handler repeats
// anyway — and because validating here would buy nothing: the launcher hides the
// window before it calls Build (internal/ui/launcher.go, submitForm), so a Build
// error surfaces as the same notification a handler error does, not as a
// corrected form. The only check that keeps the form on screen is the launcher's
// own required-field pass.
func (p *Provider) addResult(configured []string, authFor func(string) bool) providers.Result {
	fields := []providers.FormField{
		{
			Key:         "name",
			Label:       "Name",
			Placeholder: "github",
			Required:    true,
		},
		{
			Key:         "secret",
			Label:       "Secret",
			Placeholder: "base32 seed or otpauth:// URI",
			Required:    true,
			Secret:      true,
		},
	}
	if len(configured) > 1 {
		fields = append(fields, providers.FormField{
			Key:     "backend",
			Label:   "Storage",
			Options: configured,
		})
	}
	if authList := authBackends(configured, authFor); len(authList) > 0 {
		if len(configured) == 1 {
			fields = append(fields, credentialField())
		} else {
			fields = append(fields, providers.FormField{
				Key:    "credential",
				Label:  "Password (" + strings.Join(authList, ", ") + " only)",
				Secret: true,
			})
		}
	}
	return providers.Result{
		ID:       "totp:add",
		Title:    "Add TOTP code",
		Subtitle: "Paste a base32 seed or an otpauth:// URI",
		Icon:     providers.Icon{ThemeName: iconAdd},
		Category: providers.CatTOTP,
		Score:    AddScore,
		Form: &providers.Form{
			Title:  "Add TOTP code",
			Fields: fields,
			Build: func(values map[string]string) (providers.Action, error) {
				for _, f := range fields {
					if f.Required && strings.TrimSpace(values[f.Key]) == "" {
						return providers.Action{}, fmt.Errorf("%s is required", strings.ToLower(f.Label))
					}
				}
				return providers.Action{Kind: ActTOTPAdd, Values: values}, nil
			},
		},
	}
}

// authBackends returns the configured managers that authenticate per access, in
// configured order. It is a helper rather than an inline loop because the answer
// shapes both halves of the credential rule in addResult, and because it is the
// one place authFor is called for every manager — a caller that only needs the
// count must not pay for a second pass.
func authBackends(configured []string, authFor func(string) bool) []string {
	if authFor == nil {
		return nil
	}
	var out []string
	for _, name := range configured {
		if authFor(name) {
			out = append(out, name)
		}
	}
	return out
}

// setupResults offers the secrets-manager choice, skipping every name in
// exclude — nil during first-time setup, and the already-configured managers
// when the user asks for another one. An empty result therefore means "nothing
// left to configure", which is what lets the caller fall through to the
// ordinary listing rather than showing an empty chooser.
//
// The rows exist in the launcher rather than in a config file because picking a
// secrets manager is the first thing a user does with this feature and banshee
// never writes banshee.conf on the user's behalf — the choice is persisted in
// totp.json by the setup handler.
func (p *Provider) setupResults(exclude []string) []providers.Result {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	rows := []struct {
		backend  string
		title    string
		subtitle string
	}{
		{
			backend:  secrets.BackendKeyring,
			title:    "Use OS keyring (local)",
			subtitle: "Store seeds in the system Secret Service",
		},
		{
			backend:  secrets.BackendPlaintext,
			title:    "Use plaintext file (local, not recommended)",
			subtitle: "Store seeds unencrypted, readable by anything running as you",
		},
		{
			backend:  secrets.BackendNimbus,
			title:    "Nimbus (cloud — coming soon)",
			subtitle: "Not available yet",
		},
	}
	var out []providers.Result
	for _, r := range rows {
		if skip[r.backend] {
			continue
		}
		out = append(out, providers.Result{
			ID:       "totp:setup:" + r.backend,
			Title:    r.title,
			Subtitle: r.subtitle,
			Icon:     providers.Icon{ThemeName: iconEntry},
			Category: providers.CatTOTP,
			Score:    TriggerScore,
			Action:   providers.Action{Kind: ActTOTPSetup, Argv: []string{r.backend}},
		})
	}
	return out
}

// setupMoreResult is the hint row that reopens the chooser for a further secrets
// manager. It is a row rather than a documented query because nothing else would
// ever tell the user that storing seeds in two places is possible at all; it
// dispatches its own kind so the handler owns the reopen, which is the only part
// of the flow the provider cannot do from a query.
func setupMoreResult() providers.Result {
	return providers.Result{
		ID:       "totp:setup:more",
		Title:    "Add another secrets manager",
		Subtitle: "Store some codes somewhere else — Enter shows the choices",
		Icon:     providers.Icon{ThemeName: iconAdd},
		Category: providers.CatTOTP,
		Score:    SetupMoreScore,
		Action:   providers.Action{Kind: ActTOTPSetupMore},
	}
}

// credentialField is the masked per-access password input shared by the unlock
// form and the add form, so both spell the key the copy handler reads.
func credentialField() providers.FormField {
	return providers.FormField{
		Key:      "credential",
		Label:    "Password",
		Required: true,
		Secret:   true,
	}
}

// errCredentialRequired is returned by an unlock form's Build when the
// password is blank after trimming. It is defensive: the launcher's
// FirstMissingRequired pass rejects a blank required field before Build ever
// runs, so this branch is unreachable through the UI and exists for a caller
// that builds the action itself.
var errCredentialRequired = errors.New("a password is required")

// secretKey maps an entry name onto its key in a secrets Store. The "totp/"
// prefix is this package's slice of the shared key namespace documented on
// package secrets.
func secretKey(name string) string { return "totp/" + name }

// unavailable renders a backend failure as a short subtitle. It classifies the
// sentinel errors instead of printing err, both because a Store error can name
// the key and because a row is not the place for a stack of wrapping.
func unavailable(err error) string {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		return "no secret stored for this entry"
	case errors.Is(err, secrets.ErrAuthRequired):
		return "locked — authentication required"
	case errors.Is(err, secrets.ErrNotConfigured):
		return "secrets backend unavailable"
	default:
		return "code unavailable"
	}
}

// parseTrigger recognizes the "totp"/"otp" keyword and splits off whatever
// follows it. Matching is case-insensitive on the keyword only; the remainder
// keeps its original case because it is fed to the fuzzy scorer, which does
// its own folding.
func parseTrigger(q string) (triggered bool, filter string) {
	lower := strings.ToLower(q)
	switch lower {
	case "totp", "otp":
		return true, ""
	}
	for _, pre := range []string{"totp ", "otp "} {
		if strings.HasPrefix(lower, pre) {
			return true, strings.TrimSpace(q[len(pre):])
		}
	}
	return false, ""
}
