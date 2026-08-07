# banshee

A Raycast-style launcher for Hyprland that is also a declarative tmux session
manager. One Go binary, one keypress.

Type three letters of a repo name and banshee offers you, in a fixed block:
its tmux session, its GitHub page, whatever connectors you have bound to it,
and its directory — plus installed apps, running processes you might want to
kill, and anything your plugins contribute.

```
┌──────────────────────────────────────────────┐
│  blacksh                                     │
├──────────────────────────────────────────────┤
│  ▎ Open blacksheep session      /home/…      │
│    Open blacksheep on GitHub    github.com/… │
│    Open blacksheep on Railway   railway.com/…│
│    Open blacksheep directory    /home/…      │
└──────────────────────────────────────────────┘
```

From a shell, `banshee` still opens the fuzzy repo picker and builds your
JSON-described tmux session, exactly as it always has.

## Features

- **Layer-shell launcher** — GTK4 + `gtk4-layer-shell`, overlay layer,
  frosted-glass panel, keyboard-exclusive. Toggled by a Hyprland bind.
- **Resident daemon** — the window is built once and shown/hidden, so toggles
  are instant. `banshee toggle` starts the daemon itself on first use.
- **Repo-first ranking** — every result derived from a repo shares that repo's
  fuzzy score, so a matched repo produces one stable block at the top of the
  list instead of four rows scattered through it.
- **Declarative tmux sessions** — JSON describes windows, nested pane splits,
  working directories and startup commands; groups load several at once.
- **Applications and processes** — `.desktop` apps via GIO (`NoDisplay`,
  `OnlyShowIn` and `Exec` field codes handled correctly), plus
  `Kill <process>` rows with SIGTERM by default and SIGKILL on Tab.
- **Calculator** — type `2+2` (or force with `= ` / `calc `); Enter copies the
  result to the clipboard, Tab copies the whole equation.
- **Connectors and plugins** — GitHub and Railway are built in; add your own
  declarative URL connectors or long-running exec plugins.

## Requirements

| | |
|---|---|
| **Required** | Go 1.23+, `gtk4`, `gtk4-layer-shell`, `pkgconf`, `make` |
| **Compositor** | Hyprland (or any `wlr-layer-shell` compositor; banshee falls back to a normal window elsewhere) |
| **Recommended** | `tmux` (session features), `fzf` (nicer CLI picker), `git` (GitHub connector), `wl-clipboard` (calculator copy; `xclip`/`xsel` on X11) |

On Arch, the lot:
`sudo pacman -S --needed go gtk4 gtk4-layer-shell pkgconf make tmux fzf git`

## Install

```sh
git clone https://github.com/jourdanhaines/banshee
cd banshee
./install.sh
```

The installer checks dependencies, builds the binary, and wires banshee in:
`~/.local/bin/banshee`, shell integration in your `~/.zshrc` or `~/.bashrc`,
the `$menu` rebind and `layerrule` block in `~/.config/hypr/hyprland.conf`
(after a timestamped backup), a default config plus the example plugin, and an
optional, not-enabled `banshee.service` user unit. It is idempotent. Skip the
automatic edits with `--no-hyprland` / `--no-shell`, and undo everything with
`./install.sh --uninstall`. Prefer to do it by hand? `make install` touches
nothing else; apply [contrib/hyprland.conf](contrib/hyprland.conf) yourself.
Either way, verify with `banshee doctor`.

> The first build compiles the GTK bindings and takes 5–15 minutes; later
> builds are seconds. `make warm` does just this step.

## Hyprland setup

banshee needs one rule block and a `$menu` rebind. `banshee doctor` prints them,
`install.sh` applies them, [contrib/hyprland.conf](contrib/hyprland.conf)
explains them:

```
layerrule {
    name = banshee
    match:namespace = banshee
    blur = on
    ignore_alpha = 0
}
$menu = banshee toggle
```

The block syntax needs Hyprland >= 0.53; older releases take the legacy form
(`layerrule = blur, banshee` and `layerrule = ignorezero, banshee`). Either way
it only buys the blurred glass — without it the panel is opaque but usable.

Most configs already bind `$menu` to a key (`bind = $mainMod, SPACE, exec,
$menu`), so redefining `$menu` is all it takes to replace wofi or rofi. If
yours has no `$menu`, bind banshee directly instead.

### Launcher keys

| Key | Action |
|---|---|
| `Esc` | Hide |
| `↓` / `Ctrl-J` / `Ctrl-N` | Next result |
| `↑` / `Ctrl-K` / `Ctrl-P` | Previous result |
| `Enter` | Primary action |
| `Tab` / `Shift-Enter` | Alternate action (see below) |
| `Ctrl-W` | Delete the word before the cursor |

Typing always goes to the search box — the selection keys never steal it.

Session rows attach in the **last active terminal**: if a tmux client is
attached anywhere, that client switches to the session and (under Hyprland) its
window is focused; otherwise a new terminal opens. The alternate action always
opens a new terminal instead, and on `Kill <process>` rows it sends SIGKILL
rather than SIGTERM.

## Configuration

Config lives at `~/.config/banshee/banshee.conf` — `key = value`, one per line,
with `#`-comments and unrecognized keys ignored, so a config written for a newer
banshee still loads on an older one. Every option is listed and annotated in
[contrib/banshee.conf](contrib/banshee.conf), the file the installer drops in
place. `banshee reload` applies changes to a running daemon.

### Paths

| Path | Contents |
|---|---|
| `~/.config/banshee/banshee.conf` | Configuration |
| `~/.config/banshee/sessions/<target>.json` | Per-target session layout |
| `~/.config/banshee/groups/<name>.json` | Named groups of targets |
| `~/.config/banshee/plugins/<id>/manifest.json` | Plugins and connectors |
| `~/.local/share/banshee/repo_cache` | Cached repo list |
| `~/.local/share/banshee/last_action` | What `banshee -r` replays |
| `~/.local/state/banshee/daemon.log` | Daemon log |
| `$XDG_RUNTIME_DIR/banshee/banshee.sock` | Control socket (+ `.lock`) |

## Sessions

A target is either a git repo found by the indexer or a session config —
`banshee <target>` accepts both. With no config, banshee opens a plain tmux
session in the repo. With one, it builds the described layout.

`~/.config/banshee/sessions/blacksheep.json`:

```json
{
  "v": 1,
  "name": "blacksheep",
  "cwd": "~/dev/blacksheep",
  "windows": [
    { "name": "edit", "panes": [
      { "run": "nvim ." },
      [{ "run": "npm run dev" }, { "run": "npm run test:watch" }]
    ] },
    { "name": "git", "panes": [{ "run": "lazygit" }] }
  ]
}
```

Rules worth knowing:

- **The filename is authoritative.** The tmux session name is the filename with
  `.` and `:` mapped to `_`; the JSON `name` field is informational.
- **`panes` alternates direction by depth.** Depth 0 splits into columns (`-h`),
  a nested array splits the pane it replaces perpendicularly, and so on.
- **`cwd` inherits** session → window → pane, falling back to `$HOME`.
- **Loading is idempotent.** An existing session is attached, not rebuilt.

A group is just a list of targets —
`{ "v": 1, "name": "dev", "targets": ["blacksheep", "atlas"] }`. `banshee -se
<target>` and `banshee -ge <group>` create and edit both kinds of file for you,
validating on save and re-opening your editor when the JSON is wrong.

## CLI

Run `banshee --help` for the full list of commands and flags.

Inside tmux, loading a target switches the client; outside it, banshee hands the
terminal over to `tmux attach`. Without tmux, `banshee [query]` degrades to a
fuzzy repo jumper that `cd`s your shell into the match — that one only works
through the sourced `banshee.plugin.zsh`/`.bash`, since a binary cannot change
its parent's working directory.

## Plugins

A plugin is a directory under `~/.config/banshee/plugins/<id>/` with a
`manifest.json`. **`url` connectors** are declarative: bind one to a repo with
`<repo>/.banshee/config.json` and it contributes a row to that repo's block
(GitHub and Railway ship built in). **`exec` plugins** are long-running child
processes speaking newline-delimited JSON, optionally gated behind a query
prefix; their actions can open URLs, run detached commands, copy text to the
clipboard, or call back into the plugin. Start from the runnable sample in
[plugins/example/](plugins/example/); the wire protocol is defined in
[internal/providers/plugins/proto.go](internal/providers/plugins/proto.go),
and the manifest schema, URL placeholders and binding rule in
[internal/providers/connectors/manifest.go](internal/providers/connectors/manifest.go).

## Running as a systemd service

Optional — `banshee toggle` self-starts the daemon. To have it start with your
graphical session instead: `systemctl --user enable --now banshee.service`.

## Contributing

Build, test and review workflow: **[CONTRIBUTING.md](CONTRIBUTING.md)**.

## License

MIT
