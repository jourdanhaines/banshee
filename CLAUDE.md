# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Development Commands

```bash
make build       # → ./bin/banshee (CGo mandatory: GTK4 + gtk4-layer-shell are C libraries)
make test        # go test ./... — no display, tmux server or network needed
make test-race   # same suite under -race, minus internal/theme (see below)
make smoke       # go test -tags gtksmoke -run Smoke ./internal/daemon — needs a live Wayland/X session
make lint        # gofmt -l check, then go vet ./..., then golangci-lint if installed
make warm        # pre-compile the gotk4 cgo tree: 5–15 min cold, once per machine
make install     # binary + shell plugins + systemd unit + banshee.conf + example plugin
make help        # list documented targets
```

- **`make lint && make test` is the gate** — `.github/workflows/ci.yml` runs that plus `make build` on every push and PR.
- **Never clear `GOCACHE`** — it holds the warmed gotk4 cgo tree; clearing it costs another `make warm`.
- **`make test-race` excludes `internal/theme`** — reason in the Makefile's `## test-race` comment block.

## Git Conventions

- **Prefix every subject** with one of `feat`, `fix`, `chore`, `docs`, `refactor` — single word, no scope brackets, subject ≤ 60 chars (`feat: add clipboard-history provider`).
- **Subject line only** — no commit body unless the *why* is genuinely non-obvious or the user explicitly asks for one.
- **Never** use `Co-authored-by` trailers.

## Go Code Style (internal/)

- **Provider shape** — a result source is `type Provider struct{…}` in its own package under `internal/providers/<name>/`, built by a `New(...) *Provider` constructor, exposing exactly `Name() string` and `Query(ctx context.Context, q string) ([]providers.Result, error)`. Optional knobs are variadic `Option` funcs (`apps.WithMaxResults`), never extra constructor params. `Query` must honor `ctx` cancellation — the aggregator cancels the previous query on every keystroke.
- **Extension seams only at declared boundaries** — interfaces: `providers.Provider`, `providers.Aggregator`, `tmux.Runner`, `index.Index`, `daemon.UI`; registration structs: `providers.Registry`, `launch.Dispatcher`; func type: `fuzzy.Scorer`. Everything else is a concrete struct. Never introduce an interface to make a single implementation mockable; use the seam above it.
- **Action handlers register per-package** — a provider package that emits a new `Action.Kind` ships `Register<Name>Handler(d *launch.Dispatcher)` and `boot.registerHandlers` calls it (`apps.RegisterAppLaunchHandler`, `procs.RegisterKillHandler`, `plugins.RegisterCallbackHandler`, `sessions.RegisterAttachHandler`, `connectors.RegisterLinkHandler`). **No** switch on `Kind` outside `Dispatcher.Dispatch`.
- **Tests are table-driven** — one `[]struct{name string; …}` per behavior, subtests via `t.Run`. Exceptions: end-to-end flow and process-lifecycle tests (`providers/aggregator_block_test.go`, `plugins/lifecycle_test.go`, `cli/editor_test.go`), which assert one sequence rather than a matrix.
- **Tests are hermetic** — **no** live tmux server, GTK display, network, or dependence on the developer's `$HOME`. Use `t.TempDir()`, fake procfs trees, `sh -c` stub plugins, and `tmux.Runner` fakes for golden-argv assertions. A test that needs a display goes behind a build tag like `gtksmoke`.
- **Comments** — godoc on every exported identifier saying what it does and why it exists, never restating its name; plus a rationale comment on any code whose correctness depends on an external constraint (upstream bug, GTK threading, wire compatibility), see `boot.reload` on why the rescan is backgrounded.
- **Forward compatibility is not optional** — unknown config keys and unknown JSON fields are ignored everywhere. Everything on disk or on the wire carries a `v`: session configs, groups, plugin manifests, per-repo configs, the IPC protocol, the plugin protocol.

## Architecture

banshee is a single Go binary with two front-ends over one core: a GTK4 layer-shell launcher for Hyprland and a tmux session CLI. Dependency direction is strictly downward: `boot` knows every provider, no provider knows `boot`.

- `cmd/banshee/` — dispatch only.
- `internal/boot/` — assembles registry, aggregator, dispatcher, plugin host, UI constructor.
- `internal/cli/` — flag parsing, pickers, editor loop, `doctor`, `link`, hidden `_complete` / `_startup-prompt`.
- `internal/daemon/` — single-instance lock, GTK hosting, IPC op dispatch.
- `internal/ipc/` — control-socket protocol v1, client and server.
- `internal/ui/` — GTK4 window, list, rows, mode-aware keymap, debounce, form view (form logic in `formstate.go`, GTK plumbing in `form.go`; a `Result.Form` activation slides the form page in instead of dispatching, and a `FormField` carrying `Options` renders as a `gtk.DropDown` whose Enter is passed through to GTK instead of submitting — `submitFormOrPass` → `formView.focusInDropdown`, so arrows and Enter pick an option and Tab leaves the field). A `Result.Expiry` drains a `gtk.ProgressBar` rather than printing a countdown suffix: one shared `progressbar.code-timer` between the query and the list covers every standard 30 s row, because they all rotate on the same `unix%30` boundary, and only a non-standard `Result.Period` earns a thin per-row `.row-timer` bar; a single 250 ms ticker (`liveTick`) drives both from the wall clock, and the fraction math is display-free in `live.go` (`StandardPeriod`, `IsStandard`, `Fraction`, `StandardFraction`, `AnyStandardLive`). The shared bar is always allocated and toggled with `SetOpacity`, never `SetVisible`, because appearing and disappearing would break the constant panel height (min == max `resultsHeight`). A `Result.Preview` path grows the row a second storey — a `gtk.Picture` (`.result-preview`) under the title/subtitle line; the texture is decoded once per path, height-capped at decode time (`previewMaxHeight` — GTK4 CSS has no max-height) and cached on the Launcher, dropped by both `Hide` and `Reload` because the backing tmpfs files can be evicted behind it. Taller rows scroll inside the fixed-height viewport, so the panel-height invariant holds.
- `internal/theme/` — CSS generation from accent/opacity/width.
- `internal/layershell/` — wrapper over the gtk4-layer-shell binding.
- `internal/hypr/` — minimal Hyprland IPC client (focus the terminal holding a pid).
- `internal/icons/` — SVGs compiled into the binary (github, railway), accent-tinted.
- `internal/providers/` — frozen `Provider`/`Result` contract plus the aggregator.
- `internal/providers/sessions/` — "Open \<repo\> session" (`CatSession`).
- `internal/providers/lastaction/` — "Resume \<target\>" (`CatSession`).
- `internal/providers/connectors/` — GitHub, Railway, url plugins (`CatGitHub`/`CatConnector`); also the "Link \<Connector\> project to \<repo\>" form row for the current tmux pane's unbound repo, the `connector-link` action, and the `.banshee/config.json` save path.
- `internal/providers/repos/` — "Open \<repo\> directory" (`CatDirectory`).
- `internal/providers/calc/` — inline calculator (`CatCalc`).
- `internal/providers/totp/` — TOTP codes with a draining timer (`CatTOTP`): RFC 6238 + `otpauth://` parsing, the non-secret `totp.json` index, the `totp`/`otp` trigger, and the `totp-copy` / `totp-add` / `totp-setup` / `totp-setup-more` / `totp-wizard-reset` / `totp-wizard-fix` actions. Seeds live in `internal/secrets`, never in the index, and may live in several managers at once: `Index.Backends` records every manager configured after the first and `Entry.Backend` names the one holding that entry's seed (empty means the index default, which is why a single-manager file carries neither key), read together only through `Configured()` / `DefaultBackend()` / `BackendOr()` so no caller has to know the first manager is stored apart from the rest. `AddBackend` holds the compat invariant — whenever the legacy `Backend` key is populated it is `Configured()[0]`, the first manager ever configured populates it, and nothing added later moves it — because `mergeKnown` drops an `omitempty` field at its zero value, so putting the first name in `Backends` would strip `backend` from an existing file and leave an older build unable to find any seed at all (an older build reading a multi-manager file sees the default manager only and reports the rest as having no secret stored — degraded, never lost). Once more than one manager is configured the add form grows a `Storage` dropdown (`FormField.Options`, first option = the default), entry rows carry a `" · <manager>"` subtitle label, and the triggered list ends with the `totp:setup:more` hint row ("Add another secrets manager") whose `totp-setup-more` handler reopens the launcher on the internal `totp setup` chooser token — a query listing only the unconfigured managers, which deliberately shadows an entry named "setup" rather than inventing a settings surface banshee does not have. A backend-unusable failure arms the shared `SetupState` and boot's `Reopen` hook re-shows the launcher with the setup wizard's guidance/retry rows ("wizard-as-results-rows", `wizard.go`) instead of a dead-end toast; which errors route there lives on `backendUnusable` in `handlers.go`, and whether the wizard renders at all is `wizardApplies` plus the `SetupState.FailSetup` flag — a failure raised by an explicit setup or fix attempt names a manager that is by definition not configured yet and must still open the wizard, while a stale failure against a manager the user has since reconfigured away stays a toast. A `totp-wizard-fix` row runs a user-level repair in-process from the package's own `wizardFixes` table — the action carries `[backend, fixID]` as a lookup key, never argv — and finishes setup itself so a repair that worked retires the wizard without a manual Retry; privileged repairs (the package install) go out as `providers.ActTerminal` so sudo prompts in the user's own terminal and banshee never sees the password, and degrade to the distro-neutral clipboard-copy row when no `packageManagers` binary is on PATH. A fix that needs a secret from the user (`wizardFix.stdin`, e.g. creating the login keyring) opens a masked form first and pipes the submitted password from `Action.Values` to the command's stdin — never argv, never a log.
- `internal/providers/cliphist/` — session-only clipboard history behind the `clip`/`cb` trigger (any prefix of "clipboard" ≥ "clip" triggers; `CatClipboard`). Three pieces share one in-memory `Store` (ring of 1,000, byte-budgeted, consecutive duplicates collapse by sha256 into a `Copies` bump): the `Watcher` — a supervised `wl-paste --watch sh -c 'cat >/dev/null; echo'` child (drain-then-signal, so a signal line always follows the content it announces; plugin-host-style backoff/crash-limit; started by `boot.run`, never `boot.New`, cycled by reload on the `clipboard_history` key) whose per-signal capture runs `--list-types`, classifies (uri-list > png/jpeg allowlist > generic text; `x-kde-passwordManagerHint` ⇒ masked), fetches typed, and runs `LooksSecret` heuristics over text; the provider, which self-thresholds like totp (`Score = TriggerScore - index` keeps recency through the aggregator sort; **sensitive entries are excluded from filtered queries** — fuzzy-matching masked content would be a query-by-query oracle for the secret); and the `clip-copy`/`clip-delete` handlers, which carry the entry ID only. Image payloads live as files under `$XDG_RUNTIME_DIR/banshee/clips` (tmpfs; `Result.Preview` renders the path as a large in-row image while the 24px icon slot keeps a generic themed image icon — and never for a `Sensitive` entry, whose mask beats its own pixels), removed on evict/delete/`Clear` — history is memory + tmpfs, never disk. The TOTP self-copy loop is closed in boot: totp's `Copy` closure arms `Store.SuppressNext(sha256)` and copies via `launch.CopyToClipboardSensitive` (wl-copy `--sensitive`), so a code that races the suppression still lands masked.
- `internal/providers/apps/` — `.desktop` applications (`CatApp`).
- `internal/providers/steam/` — installed Steam games and the Steam store. Each installed game (scanned from `libraryfolders.vdf` + `appmanifest_*.acf` via a hand-rolled KeyValues parser, cached at construction and rescanned on `banshee reload`) emits a fixed-order 4-row block — Play / library / store page / SteamDB (`CatSteamPlay`..`CatSteamDB`, shared score, repo-block pattern). The `steam` trigger lists all games; with a filter it also queries the storefront search API (bounded by a 1.5 s timeout and the query ctx, degrading to installed rows + the trailing "Search Steam store" row on any failure — `CatSteamStore`/`CatSteamSearch`). Every action is `ActURL` (`steam://` deep links, https pages), so the package registers no handler. Game icons are absolute `Icon.Path`s into Steam's `librarycache`, falling back to the builtin accent-tinted `steam` mark.
- `internal/providers/procs/` — "Kill \<process\>" (`CatKill`).
- `internal/providers/plugins/` — exec-plugin host, protocol v1 (`CatPlugin`).
- `internal/launch/` — `Action.Kind` → handler dispatch table.
- `internal/fuzzy/` — `Score(query, candidate) (int, bool)`.
- `internal/index/` — repo discovery plus cache file.
- `internal/session/` — session/group JSON schema, validation, resolution.
- `internal/tmux/` — `Runner` interface plus session builder.
- `internal/state/` — `last_action` store.
- `internal/secrets/` — `Store` seam over secret backends (`plaintext`, `keyring`, `nimbus` stub) plus `Open(name)`; keys are namespaced by their owner (`totp/<name>`) and the package never enumerates them. `Store.Blocking()` marks a backend whose calls can wait on an unlock prompt or a network round trip — those must be detached off the GTK main loop, never keyed off `AuthPerAccess()`.
- `internal/config/` — `banshee.conf` parser, XDG paths, per-repo config.

### Boot flow

`cmd/banshee/main.go` → `cli.Run(os.Args[1:], boot.Hooks())` → `boot.New(cfg)` registers providers into `providers.Registry` and handlers into `launch.Dispatcher` → `daemon.Run(daemon.Options{NewUI: …})` with a UI constructor function.

- **`daemon` never imports `internal/ui`** — it takes a `func(*gtk.Application) daemon.UI`, which is what lets an alternate front-end reuse the daemon.
- **`cli` never imports GTK** — launcher verbs (`daemon`, `toggle`, `show`, `hide`, `reload`, `quit`) reach it as `cli.Hooks` function values, which is what keeps the CLI testable with no display.

### The two registries

- **`providers.Registry` is append-only** — `Register` appends; there is no removal. Swapping a whole provider set at runtime needs an indirection (see `boot.pluginSet`, which reads through to the plugin host so `banshee reload` can replace every exec plugin).
- **`launch.Dispatcher` replaces by `Action.Kind`** — `Register` overwrites. Re-running `boot.registerHandlers` is therefore the reload path for a changed `terminal =`.

### Shared-score contract

Every repo-derived provider (sessions, GitHub, connectors, repos) scores the **repo basename** — `fuzzy.Score(query, repo.Name)` — with the same `Scorer`, never its own rendered title. Identical scores let the `Category` tiebreak in the `(-Score, Category, Title)` sort collapse one repo's rows into a fixed-order block; scoring `"Open blacksheep on GitHub"` would scatter it. Asserted end-to-end in `internal/providers/aggregator_block_test.go`; full contract in the `internal/providers/aggregator.go` doc comment. One deliberate exception: the connectors provider's link row is connector-derived and scores the connector name/id (rationale on `linkResults`).

Results at `CatApp` or lower priority (apps, procs, plugins) are dropped below `MinScore` on non-empty queries so weak matches cannot outrank a repo block. Empty queries are never thresholded.

### Frozen contracts

These files define cross-package boundaries and change **only** by a deliberate migration that touches every consumer — never a drive-by edit.

- `internal/providers/provider.go` — `Result`, `Action`, `Icon`, `Category`, `Form`, `FormField`, `Provider`, `Registry`. (Migration 2026-08: `Result.Form` and `Action.Values` added for in-launcher forms — additive, zero value inert. Migration 2026-08b: `FormField.Secret`, `Result.Expiry` and `CatTOTP` added for the TOTP tool — additive, zero value inert. Migration 2026-08c: `Result.Period` and `FormField.Options` added — additive, zero value inert. Migration 2026-08f: `Result.Preview` added — additive, zero value inert.)
- `internal/providers/aggregate.go` — `Aggregator`.
- `internal/ipc/proto.go` — protocol v1, socket and lock paths.
- `internal/config/config.go` — `Config`, defaults, XDG paths. (Migration 2026-08b: additive path funcs `TOTPIndexPath()`, `SecretsDir()`, `PlaintextSecretsPath()` — new well-known locations, no schema change.)
- `internal/config/repoconf.go` — per-repo `.banshee/config.json`.
- `internal/session/schema.go` — `Session`, `Window`, `Pane`, `Group`.
- `internal/tmux/runner.go` — `Runner`, `ExecRunner`, `SessionName`.
- `internal/index/index.go` — `Index`, `Repo`.
- `internal/launch/dispatch.go` — `Dispatcher`.
- `internal/layershell/layershell.go` — `Setup`, `SetKeyboardMode`, `Supported`.

## Plugins

- **Manifest** — every plugin is a directory `<plugins-dir>/<id>/manifest.json` (schema `v: 1`); `config.PluginsDir()` resolves the location. The directory name is cosmetic — the manifest `id` is the identity. A malformed manifest disables only its own plugin and is reported by `banshee doctor`.
- **Two types** — `"url"` is a declarative connector (a URL template plus optional binding from the repo's `.banshee/config.json`, no Go at all); `"exec"` is a long-running child process speaking newline-delimited JSON, protocol v1, on stdin/stdout.
- **Source of truth** — `internal/providers/plugins/proto.go` for the wire format, event set and the exec process contract (cwd, `BANSHEE_PLUGIN_*` env, stdout hygiene); `internal/providers/plugins/exec.go` for the lifecycle and timing constants (soft timeout, crash backoff, disable-until-reload, shutdown grace); `internal/providers/connectors/manifest.go` for the manifest schema of both types, plus the URL placeholders and binding rule; `plugins/example/` for a complete commented exec plugin. Do not restate any of it elsewhere.

## Gotchas

- **Never connect `gtk.CSSProvider`'s `parsing-error` signal** — under gotk4 `pkg/v0.4.0` the generated marshaller double-frees the `GError` and aborts the process when it fires. GTK already logs parse errors through GLib. `internal/theme/load_test.go` validates the generated stylesheet instead by parsing it with GTK's own engine and asserting every selector and declaration survives the round trip.
- **Test the launcher from a fresh shell** — stale shell state (an old `banshee` on `$PATH`, a running daemon from a previous build, a sourced shell plugin) produces errors that look like code bugs. A new session, after `make install`, is the only reliable check.
- **Secret material never reaches argv or a log** — TOTP seeds, computed codes and form credentials transit daemon memory and `Action.Values`, so **never log `Action.Values`** (a `Secret: true` field's value rides in it) and never place secret material on a command line. Clipboard writes and secret-store writes go through stdin (`launch.CopyToClipboard`, the `internal/secrets` backends); errors name the key or the backend, never the value.
- **GTK is single-threaded** — everything in `internal/ui` runs on the GTK main loop, and the daemon's socket goroutine marshals every op through `glib.IdleAdd` (5 s timeout). Providers run off the main loop concurrently; action handlers run on it, after the window is hidden, and must spawn and return rather than block.
