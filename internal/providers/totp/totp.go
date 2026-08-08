package totp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
)

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
//     lists every entry, plus the "Add TOTP code" row last.
//   - Without the trigger, entry names are fuzzy-matched and only positive
//     matches appear, so typing a repo name never scatters codes into the list.
//   - With no backend chosen yet, the trigger offers the three setup rows
//     instead of entries; without the trigger it stays silent.
//   - While a backend failure is recorded in the shared SetupState, the trigger
//     renders the setup wizard instead of any of the above.
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
	// A recorded backend failure replaces every other row, but only under the
	// trigger: an untriggered query that merely fuzzy-matches an entry name must
	// never leak a backend diagnosis into a list the user did not ask TOTP for.
	// The backend guard drops a stale wizard — a failure recorded against a
	// backend the user has since reconfigured out of band no longer describes
	// what they would be using, so the ordinary rows win. A wizard raised while
	// no backend is persisted is still first-time setup, which is the only
	// situation where the "choose a different backend" escape row belongs; that
	// is the firstTime argument. Snapshot is nil-receiver-safe, so a provider
	// with no SetupState wired up needs no check of its own.
	if triggered {
		if backend, message, ok := p.setup.Snapshot(); ok && wizardApplies(idx.Backend, backend) {
			return wizardResults(backend, message, idx.Backend == "", p.lookPath), nil
		}
	}
	if idx.Backend == "" {
		if !triggered {
			return nil, nil
		}
		return p.setupResults(), nil
	}

	store, err := p.open(idx.Backend)
	if err != nil {
		return nil, err
	}
	auth := store.AuthPerAccess()
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
		row, err := p.entryResult(ctx, store, auth, e, score, now)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if triggered {
		out = append(out, p.addResult(auth))
	}
	return out, nil
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

// entryResult builds one entry's row.
//
// For a local backend the code is computed here so the row shows it directly,
// and Expiry is set so the UI can tick a countdown and re-query when the code
// rotates. A backend that authenticates per access gets no code at all: asking
// for the password once per keystroke would be absurd, so the row carries a
// masked credential form and defers everything to activation.
//
// A backend failure for one entry degrades that row to an explanatory subtitle
// rather than failing the query — one unreadable key must not blank the list.
// That includes a read that ran past queryTimeout, which is why the abort check
// consults the caller's ctx rather than the bounded one: only a cancelled query
// aborts, because that means the user has already typed the next character.
func (p *Provider) entryResult(ctx context.Context, store secrets.Store, auth bool, e Entry, score int, now time.Time) (providers.Result, error) {
	res := providers.Result{
		ID:       "totp:" + e.Name,
		Title:    e.Name,
		Icon:     providers.Icon{ThemeName: iconEntry},
		Category: providers.CatTOTP,
		Score:    score,
		Action:   providers.Action{Kind: ActTOTPCopy, Argv: []string{e.Name}},
	}
	if auth {
		name := e.Name
		res.Subtitle = "Enter to unlock and copy"
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

	params := e.Params()
	getCtx, cancel := context.WithTimeout(ctx, p.queryTimeout)
	defer cancel()
	secret, err := store.Get(getCtx, secretKey(e.Name), secrets.Credential{})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return providers.Result{}, ctxErr
		}
		res.Subtitle = unavailable(err)
		return res, nil
	}
	raw, err := DecodeSecret(secret)
	if err != nil {
		res.Subtitle = "stored secret is not valid base32"
		return res, nil
	}
	code, err := Code(raw, now, params)
	if err != nil {
		res.Subtitle = unavailable(err)
		return res, nil
	}
	res.Subtitle = FormatCode(code)
	res.Expiry = ExpiryOf(now, params.Period)
	return res, nil
}

// addResult is the row that opens the "add a code" form. It is emitted only
// under the trigger — an unfiltered list of every launcher query would carry a
// row nobody asked for — and always last, at AddScore.
//
// Build only checks that the fields are non-empty. Everything else (base32
// validity, otpauth parsing, duplicate names) is the handler's job, because a
// mistyped seed is worth explaining after the fact rather than blocking
// submission on a parse the handler repeats anyway — and because validating
// here would buy nothing: the launcher hides the window before it calls Build
// (internal/ui/launcher.go, submitForm), so a Build error surfaces as the same
// notification a handler error does, not as a corrected form. The only check
// that keeps the form on screen is the launcher's own required-field pass.
func (p *Provider) addResult(auth bool) providers.Result {
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
	if auth {
		fields = append(fields, credentialField())
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

// setupResults offers the one-time backend choice. The rows exist in the
// launcher rather than in a config file because picking a secrets backend is
// the first thing a user does with this feature and banshee never writes
// banshee.conf on the user's behalf — the choice is persisted in totp.json by
// the setup handler.
func (p *Provider) setupResults() []providers.Result {
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
	out := make([]providers.Result, 0, len(rows))
	for _, r := range rows {
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
