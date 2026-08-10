// Package providers defines the core contract between result providers and
// the launcher: every source of launchable results (tmux sessions, repos,
// applications, processes, connectors, external plugins) implements Provider
// and registers itself with a Registry.
//
// This file is a frozen Phase-0 contract. Changing any exported type here
// requires touching every provider — prefer adding new optional fields over
// modifying existing ones.
//
// Migration 2026-08b: CatTOTP, FormField.Secret and Result.Expiry added for
// the built-in TOTP service — additive, zero value inert. A provider that
// never sets them behaves exactly as before, and a UI that ignores them
// renders every field unmasked and every row static.
//
// Migration 2026-08c: Result.Period and FormField.Options added — additive,
// zero value inert. Period refines Expiry with the length of the window that
// ends there, so the UI can show how much of it remains; Options turns a form
// field into a fixed-choice dropdown.
//
// Migration 2026-08d: CatSteamPlay, CatSteamLibrary, CatSteamStorePage,
// CatSteamDB, CatSteamStore and CatSteamSearch added for the built-in Steam
// provider — additive, zero value inert.
package providers

import (
	"context"
	"syscall"
	"time"
)

// Category orders results in the launcher list. Lower value = higher priority
// within an equal fuzzy score. Gaps are deliberate so future categories can
// slot in without renumbering.
type Category int

const (
	CatSession   Category = 0  // "Open <repo> session"
	CatGitHub    Category = 10 // "Open <repo> on GitHub"
	CatConnector Category = 20 // "Open <repo> on <Connector>"
	CatDirectory Category = 30 // "Open <repo> directory"
	CatCalc      Category = 35 // inline calculator answer
	CatTOTP      Category = 37 // inline TOTP code rows; sits above CatApp so the MinScore threshold never drops them
	CatApp       Category = 40 // installed applications
	// The four Steam block categories collapse one installed game's rows into a
	// fixed-order block, exactly like the repo block: all four rows carry the
	// game's shared score and the Category tiebreak orders them. They sit above
	// CatApp so weak game matches are MinScore-thresholded like apps.
	CatSteamPlay      Category = 41 // "Play <game>"
	CatSteamLibrary   Category = 42 // "Open <game> in Steam library"
	CatSteamStorePage Category = 43 // "View <game> on Steam store"
	CatSteamDB        Category = 44 // "View <game> on SteamDB"
	CatSteamStore     Category = 45 // live Steam store search result
	CatSteamSearch    Category = 46 // "Search Steam store for '<q>'"
	CatKill           Category = 50 // "Kill <proc>"
	CatPlugin         Category = 60 // exec-plugin results
)

// Icon identifies how the UI should resolve a result's icon. Exactly one
// field should be set; zero value means no icon.
type Icon struct {
	// ThemeName is an icon-theme name, e.g. "network-wireless-symbolic".
	ThemeName string
	// Path is an absolute path to an image file (plugin-dir icons).
	Path string
	// AppID is a .desktop application ID whose GIcon should be used,
	// e.g. "org.mozilla.firefox.desktop". Resolved by the UI via gio.
	AppID string
	// Builtin names an icon compiled into the binary (internal/icons),
	// e.g. "github". Rendered tinted with the theme accent.
	Builtin string
}

// Action kinds understood by the launch dispatcher. New kinds are added by
// registering a handler in internal/launch — never by switch sprawl.
const (
	ActExecDetach     = "exec-detach"     // Argv, detached via setsid
	ActTerminal       = "terminal"        // Argv run inside the user's terminal
	ActURL            = "url"             // URL opened with default handler
	ActSignal         = "signal"          // Sig sent to Pid
	ActPluginCallback = "plugin-callback" // activate event sent to PluginID/ResultID
	ActSession        = "session"         // Target attached in the last active terminal, or a new one
	ActClipboardCopy  = "clipboard-copy"  // Text written to the system clipboard
)

// Action describes what happens when a result is activated. Kind selects the
// dispatcher; the other fields are kind-specific payload.
type Action struct {
	Kind string

	Argv []string       // exec-detach, terminal
	URL  string         // url
	Pid  int            // signal
	Sig  syscall.Signal // signal

	PluginID string // plugin-callback
	ResultID string // plugin-callback

	Target   string // session: banshee target / tmux session name
	ForceNew bool   // session: always spawn a new terminal instead of reusing a client

	Text string // clipboard-copy: the text to copy

	// Values carries a submitted form's field values, keyed by FormField.Key.
	// Nil for actions that did not come from a form.
	Values map[string]string
}

// FormField is one input in a Form. It is purely declarative so the exec
// plugin protocol can carry it over the wire verbatim.
type FormField struct {
	// Key is the submitted-values map key; unique within the form.
	Key string
	// Label is shown above the input.
	Label string
	// Placeholder is the input's placeholder text.
	Placeholder string
	// Required refuses submission while the trimmed value is empty.
	Required bool
	// Secret renders the input masked (no echoed characters), for passwords
	// and TOTP seeds typed in front of a screen. It is a display property
	// only: the submitted value still travels verbatim in Action.Values.
	Secret bool
	// Options, when non-empty, renders the field as a fixed-choice dropdown
	// instead of a text entry: the submitted value is exactly the selected
	// option string and the first option is preselected, so the field always
	// submits a value and Required is trivially satisfied. Secret is ignored
	// on a dropdown — a fixed choice list has nothing to mask.
	Options []string
}

// Form makes a result open a secondary input view inside the launcher
// instead of dispatching immediately. Fields are declarative; Build is the
// single non-serializable part and turns the submitted values into the
// Action to dispatch — for plugin results the host synthesizes it as a
// values-carrying plugin callback.
type Form struct {
	Title  string
	Fields []FormField
	Build  func(values map[string]string) (Action, error)
}

// Result is a single launcher row.
type Result struct {
	// ID is stable within one query generation, unique per provider.
	ID       string
	Title    string
	Subtitle string
	Icon     Icon
	Category Category
	// Score is the fuzzy match score. Repo-derived results for the same repo
	// share the repo's score so they form a fixed-order block. The aggregator
	// sorts by (-Score, Category, Title).
	Score int
	// Accent is an optional CSS color (from a connector/plugin manifest)
	// applied to the row's badge.
	Accent    string
	Action    Action
	AltAction *Action // Tab / Shift+Enter
	// Form, when non-nil, replaces the primary activation: instead of
	// dispatching Action, the launcher opens an input form and dispatches
	// Build's action on submit. AltAction still dispatches directly (or does
	// nothing when nil), bypassing the form.
	Form *Form
	// Expiry, when non-zero, marks the row's displayed content as valid only
	// until that instant — a rotating TOTP code, say. The UI drains a progress
	// bar toward it and re-runs the current query once it passes, so the row
	// refreshes without the user touching the keyboard. Zero means a static
	// row.
	Expiry time.Time
	// Period, when non-zero, is the length of the validity window that ends at
	// Expiry, so the UI can render how much of it remains. Zero on a live row
	// means the standard 30-second TOTP window; zero on a static row is inert.
	Period time.Duration
}

// Provider is a source of results. Query must honor ctx cancellation: the
// aggregator cancels the previous query on every keystroke. An empty query
// means "show defaults" — providers may return nothing for it.
type Provider interface {
	Name() string
	Query(ctx context.Context, q string) ([]Result, error)
}

// Registry holds the active provider set. The daemon builds one at boot;
// adding a provider to the launcher is one Register call.
type Registry struct {
	providers []Provider
}

// NewRegistry returns an empty Registry ready for Register calls.
func NewRegistry() *Registry { return &Registry{} }

// Register appends p to the provider set. Registration order is preserved and
// is the order providers are queried in.
func (r *Registry) Register(p Provider) { r.providers = append(r.providers, p) }

// Providers returns registration-ordered providers.
func (r *Registry) Providers() []Provider { return r.providers }
