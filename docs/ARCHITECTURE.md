# Architecture

banshee is a single binary with two front-ends over one shared core: a GTK4
layer-shell launcher and a terminal CLI. This document is the map — where code
lives, how a keystroke becomes a running tmux session, and where to add things.

## Package map

```
cmd/banshee/              dispatch only: cli.Run(os.Args[1:], boot.Hooks())

internal/boot/            assembles the launcher: registry, aggregator,
                          dispatcher, plugin host, UI constructor
internal/cli/             flag parsing, pickers, editor loop, doctor,
                          hidden _complete / _startup-prompt subcommands
internal/daemon/          single-instance lifecycle, GTK hosting, op dispatch
internal/ipc/             control-socket protocol v1, client and server
internal/ui/              GTK4 window, list, rows, keymap, debounce
internal/theme/           CSS generation from accent/opacity/width
internal/layershell/      wrapper over the gtk4-layer-shell binding

internal/providers/       frozen Provider/Result contract + the aggregator
  ├── sessions/           "Open <repo> session"          (CatSession)
  ├── lastaction/         "Resume <target>"              (CatSession)
  ├── connectors/         GitHub, Railway, url plugins   (CatGitHub/CatConnector)
  ├── repos/              "Open <repo> directory"        (CatDirectory)
  ├── apps/               .desktop applications          (CatApp)
  ├── procs/              "Kill <process>"               (CatKill)
  └── plugins/            exec-plugin host, protocol v1  (CatPlugin)

internal/launch/          Action.Kind → handler dispatch table
internal/fuzzy/           Score(query, candidate) (int, bool)
internal/index/           repo discovery + cache file
internal/session/         session/group JSON schema, validation, resolution
internal/tmux/            Runner interface + session builder
internal/state/           last_action store
internal/config/          banshee.conf parser, XDG paths, per-repo config
```

Dependency direction is strictly downward: `boot` knows every provider, no
provider knows about `boot`. `cli` never imports `daemon`, `ui` or GTK — the
launcher verbs reach it as `cli.Hooks` function values, which is what keeps the
CLI testable with no display.

## Data flow

### Launcher: keystroke → result

```
Entry ::changed
  └─ 30 ms debounce                       internal/ui/debounce.go
      └─ cancel the previous query's ctx
          └─ goroutine: Aggregator.Query(ctx, q)
              └─ errgroup fan-out to every registered Provider
                  ├─ sessions   ─┐
                  ├─ lastaction  │ all score the *repo basename*
                  ├─ connectors  │ with the same Scorer
                  ├─ repos      ─┘
                  ├─ apps, procs, plugins  (own scoring, thresholded)
                  └─ sort (-Score, Category, Title), cap at max_results
          └─ glib.IdleAdd → rebuild the ListBox
              (results from a superseded generation are dropped)
```

A provider that fails is logged and dropped; it never fails the query or
cancels its siblings. A provider that is slow is cancelled by the next
keystroke.

### Launcher: Enter → running session

```
Enter
  └─ Launcher.Activate(alt)
      └─ window.Hide()            ← always first, so the terminal gets focus
          └─ Dispatcher.Dispatch(result.Action)
              ├─ terminal      → <ghostty|kitty|…> -e banshee <target>
              ├─ exec-detach   → setsid, stdio → /dev/null
              ├─ url           → xdg-open
              ├─ app-launch    → gio AppInfo.Launch
              ├─ kill-procs    → syscall.Kill (SIGTERM; SIGKILL on Tab)
              └─ plugin-callback → activate event to the owning exec plugin
```

Failures surface as a desktop notification rather than a dead keypress.

### CLI: `banshee <target>` → tmux

```
cli.App.open
  └─ session.Resolver.Resolve(target, mode, attach)
      ├─ config exists          → tmux.Builder.BuildSession
      ├─ no config, -s          → editor loop, then build
      └─ no config, repo exists → plain session in the repo
          └─ state.Store.Record  (keeps `banshee -r` coherent)
              └─ tmux.Builder.AttachOrSwitch
                  ├─ $TMUX set → switch-client
                  └─ else      → syscall.Exec tmux attach (TTY handover)
```

The launcher reaches the same code by spawning `banshee <target>` in a
terminal, so a session started from the GUI and one started from a shell are
byte-identical.

### Daemon lifecycle

```
banshee toggle
  ├─ ipc.Ping()
  │    ok            → ipc.Send(toggle)
  │    ErrNotRunning → SpawnDetached: setsid `banshee daemon`,
  │                    stdio → ~/.local/state/banshee/daemon.log
  │                    then poll ping every 50 ms for up to 3 s
  └─ daemon: flock(banshee.lock) → gtk.Application → ipc.Listen
       socket goroutine ──glib.IdleAdd──▶ main loop ──▶ handleOp(ui, req)
```

`ping` reports not-ready until the UI exists, so a cold `banshee toggle` waits
for the launcher rather than racing it. The window is hidden, never destroyed,
so every toggle after the first is instant.

## Ranking

Categories are ascending priority (`internal/providers/provider.go`), with
deliberate gaps so new ones slot in without renumbering:

| Category | Value | Source |
|---|---|---|
| `CatSession` | 0 | sessions, lastaction |
| `CatGitHub` | 10 | connectors (built-in GitHub) |
| `CatConnector` | 20 | connectors (Railway, url plugins) |
| `CatDirectory` | 30 | repos |
| `CatApp` | 40 | apps |
| `CatKill` | 50 | procs |
| `CatPlugin` | 60 | exec plugins |

Results sort by `(-Score, Category, Title)`.

**The shared-score contract.** Every repo-derived provider must score the *repo
basename* — `fuzzy.Score(query, repo.Name)` — not its own rendered title.
Because the scores are then identical, the category tiebreak collapses the four
rows for one repo into a fixed-order block at the top of the list. Scoring
`"Open blacksheep on GitHub"` instead of `"blacksheep"` would yield a different
number and scatter the block. This is asserted end-to-end in
`internal/providers/aggregator_block_test.go`.

Providers at `CatApp` and below in priority (apps, procs, plugins) are dropped
below `MinScore` on non-empty queries, so weak matches cannot outrank a repo
block. Empty queries are never thresholded: each provider decides its own
default view (resume row, running tmux sessions, top applications).

## Extension points

Each seam is an interface or a registration call, not a switch statement.

| To add… | Do this |
|---|---|
| A result category | New package under `internal/providers/<name>/` implementing `Provider`; one `Register` line in `internal/boot`. |
| An action kind | `dispatcher.Register("my-kind", handler)` in `internal/launch` or in your provider package. No existing code changes. |
| A URL connector | A `manifest.json` with `"type": "url"` — no Go at all. See `docs/PLUGINS.md`. |
| A result source with its own process | A `manifest.json` with `"type": "exec"` speaking protocol v1. |
| An alternate front-end (TUI, …) | Consume `providers.Aggregator` and `launch.Dispatcher`; implement `daemon.UI` and pass a constructor to `daemon.Run`. |
| A different fuzzy algorithm | Rewrite `internal/fuzzy.Score`. Every provider takes a `Scorer`. |
| A tmux test double | Implement `tmux.Runner`; the builder is asserted with golden argv. |

### Versioned schemas

Everything on disk or on the wire carries a `v`: session configs, groups,
plugin manifests, per-repo configs, the IPC protocol, the plugin protocol.
Unknown keys and unknown JSON fields are ignored everywhere, so a config
written for a newer banshee still loads on an older one.

### Frozen contracts

These files define cross-package boundaries and change only with a deliberate
migration:

```
internal/providers/provider.go     Result, Action, Icon, Category, Provider, Registry
internal/providers/aggregate.go    Aggregator
internal/ipc/proto.go              protocol v1, socket and lock paths
internal/config/config.go          Config, defaults, XDG paths
internal/config/repoconf.go        per-repo .banshee/config.json
internal/session/schema.go         Session, Window, Pane, Group
internal/tmux/runner.go            Runner, ExecRunner, SessionName
internal/index/index.go            Index, Repo
internal/launch/dispatch.go        Dispatcher
internal/layershell/layershell.go  Setup, SetKeyboardMode, Supported
```

## Concurrency rules

- **GTK is single-threaded.** Everything in `internal/ui` runs on the GTK main
  loop. The daemon's socket goroutine marshals every op through `glib.IdleAdd`
  and waits for the result (5 s timeout).
- **Providers are called off the main loop**, concurrently, and must honor
  `ctx` cancellation promptly — the aggregator cancels the previous query on
  every keystroke.
- **Action handlers run on the main loop**, after the window is hidden. They
  must spawn and return, never block.
- **`index.Scanner` is safe for concurrent use**; the launcher rescans in the
  background when shown with a stale cache.

## Known gotcha

Do not connect `gtk.CSSProvider`'s `parsing-error` signal under gotk4
`pkg/v0.4.0`: the generated marshaller double-frees the `GError` and aborts the
process when it fires. GTK already logs parse errors through GLib. The
generated stylesheet is instead validated at build time by
`internal/theme/load_test.go`, which parses it with GTK's own engine and
asserts that every selector and declaration survives the round trip.
