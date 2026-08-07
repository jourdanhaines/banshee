# Contributing to banshee

Thanks for looking. Start with [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for
the package map and data flow, and [docs/PLUGINS.md](docs/PLUGINS.md) if you
are extending banshee from the outside rather than changing it.

## Getting set up

```sh
sudo pacman -S --needed go gtk4 gtk4-layer-shell pkgconf make tmux fzf git
git clone https://github.com/jourdanhaines/banshee && cd banshee
make warm      # compiles the gotk4 cgo tree: 5-15 minutes, once
make test
make build     # → ./bin/banshee
```

`make warm` is the only slow step, and only the first time. **Never clear
`GOCACHE`** unless you want to sit through it again.

`CGO_ENABLED=1` is required and set by the Makefile — GTK4 and
gtk4-layer-shell are C libraries.

## Everyday commands

| Command | What it does |
|---|---|
| `make build` | Build `./bin/banshee` |
| `make test` | Full unit suite — no display, tmux server or network needed |
| `make test-race` | The same suite under the race detector (minus `internal/theme` — see below) |
| `make lint` | gofmt check, `go vet`, then golangci-lint if installed |
| `make fmt` | `gofmt -w .` |
| `make smoke` | GTK smoke test — needs a live Wayland/X session |
| `make install` | Install binary, shell plugins, unit, example plugin |
| `make uninstall` | Remove them again (configs are kept) |
| `make help` | List the documented targets |

`make lint && make test` is the gate. Both must be clean before a PR;
[.github/workflows/ci.yml](.github/workflows/ci.yml) runs exactly that on every
push and pull request, so a red PR is a red check.

`make test-race` skips `internal/theme`. `-race` implies `-d=checkptr`, and
gotk4 v0.4.0's weak-reference helper trips it from GTK's toggle-notify
callback — so any test that constructs a real GObject aborts the whole run.
That package's CSS round-trip test is the only one outside the `gtksmoke` tag
that does; `make test` covers it.

## House style

- **Idiomatic, gofmt'd Go.** Godoc comments on every exported identifier —
  say what it does and why it exists, not what its name already says.
- **Table-driven tests.** One `[]struct{name string; …}` per behavior, subtests
  via `t.Run`.
- **Tests must be hermetic.** No live tmux server, no GTK display, no network,
  no dependence on the developer's `$HOME`. Use `t.TempDir()`, fake procfs
  trees, `sh -c` stub plugins, and the `tmux.Runner` interface for golden-argv
  assertions. If a test needs a display, put it behind a build tag like the
  `gtksmoke` one.
- **Forward compatibility is not optional.** Unknown config keys and unknown
  JSON fields are ignored, everywhere. A config written for a newer banshee
  must still load on an older one.
- **Extend through the seams.** `Registry`, `Dispatcher`, `Runner`, `Index`,
  `Aggregator`, `Scorer` — never hardcode across a package boundary. Adding a
  result category should touch one new package plus one line in
  `internal/boot`.
- **Respect the frozen contracts** listed in
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md#frozen-contracts). Changing them
  is a deliberate migration, not a drive-by.

### Commit messages

Conventional commits, single line, no body unless the *why* is genuinely
non-obvious:

```
feat: add clipboard-history provider
fix: keep entry focus after an on-demand keyboard-mode toggle
docs: document the shared-score contract
```

## Manual checklist

The GTK front-end is untested by design — the automated suite covers the pure
logic (keymap, selection math, icon resolution, badge markup, CSS generation)
but not the widgets. Walk this before releasing anything that touches
`internal/ui`, `internal/theme`, `internal/daemon` or the installer.

**Launcher**

- [ ] Cold `banshee toggle` starts the daemon and shows the window (< 3 s).
- [ ] Warm toggle is visually instant (< 100 ms).
- [ ] Panel is blurred with the banshee `layerrule` block present, and still
      readable with it removed.
- [ ] Focus lands in the entry on **every** show — test both
      `keyboard_mode = exclusive` and `on-demand`.
- [ ] Typing is never swallowed; `Esc`, `Enter`, `Tab`, `Shift+Enter`,
      `Ctrl-J`/`Ctrl-K`, `↑`/`↓` all behave.
- [ ] Long titles ellipsize instead of widening the panel.
- [ ] Panel width tracks `launcher_width`.

**Ranking**

- [ ] A repo prefix (e.g. `blacksh`) yields the four-row block in order:
      session, GitHub, connectors, directory.
- [ ] Empty query shows the resume row, then running tmux sessions, then apps.
- [ ] App and process rows never outrank a matched repo block.

**Actions**

- [ ] A session row spawns the terminal and attaches to tmux.
- [ ] A directory row opens the file manager.
- [ ] A GitHub row opens the right URL.
- [ ] `Tab` on a kill row escalates SIGTERM → SIGKILL.
- [ ] An unregistered action kind surfaces as a notification, not a silent
      no-op.

**Config and lifecycle**

- [ ] Editing `accent`, `window_opacity` or `launcher_width` and running
      `banshee reload` restyles the live window.
- [ ] `banshee reload` picks up a newly cloned repo and a newly added plugin.
- [ ] `banshee quit` exits cleanly; the socket is unlinked and the lock
      released.
- [ ] A second `banshee daemon` reports "already running" and exits 0.

**CLI**

- [ ] `banshee doctor` is clean, and its Hyprland snippet matches
      `contrib/hyprland.conf`.
- [ ] `banshee -l`, `-r`, `-s`, `-se`, `-g`, `-ge` behave as in v0.3.
- [ ] Tab completion works in both zsh and bash.

**Installer**

- [ ] `./install.sh` on a clean machine wires up binary, shell rc, and
      hyprland.conf, and takes a timestamped backup before editing.
- [ ] Running it a second time changes nothing.
- [ ] `./install.sh --uninstall` removes what it added.

## Reporting bugs

Include the output of `banshee doctor`, the tail of
`~/.local/state/banshee/daemon.log`, your compositor and version, and — for
layout bugs — the session JSON that reproduces it.
