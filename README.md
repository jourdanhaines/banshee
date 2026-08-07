# banshee

A Raycast-style launcher for Hyprland that is also a declarative tmux session
manager. One Go binary, one keypress.

Type three letters of a repo name and banshee offers you, in a fixed block:
its tmux session, its GitHub page, whatever connectors you have bound to it,
and its directory — plus installed applications, running processes you might
want to kill, and anything your plugins contribute.

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

The terminal side is unchanged from v0.3: `banshee` from a shell still opens
the fuzzy repo picker and builds your JSON-described tmux session.

---

## Features

- **Layer-shell launcher** — GTK4 + `gtk4-layer-shell`, overlay layer,
  frosted-glass panel, keyboard-exclusive. Toggled by a Hyprland bind.
- **Resident daemon** — the window is built once and shown/hidden, so toggles
  are instant. `banshee toggle` starts the daemon itself on first use; no
  systemd unit required.
- **Repo-first ranking** — every result derived from a repo shares that repo's
  fuzzy score, so a matched repo produces one stable block at the top of the
  list instead of four rows scattered through it.
- **Declarative tmux sessions** — `~/.config/banshee/sessions/<target>.json`
  describes windows, nested pane splits, working directories and startup
  commands. Idempotent: loading a session that already exists attaches to it.
- **Session groups** — one name loads several targets at once.
- **Applications and processes** — `.desktop` apps via GIO (so `NoDisplay`,
  `OnlyShowIn` and `Exec` field codes are handled correctly), plus
  `Kill <process>` rows with SIGTERM by default and SIGKILL on Tab.
- **Calculator** — type `2+2` (or force with `= ` / `calc `); Enter copies the
  result to the clipboard, Tab copies the whole equation. Needs `wl-clipboard`
  (or `xclip`/`xsel` on X11).
- **Connectors and plugins** — GitHub and Railway are built in; add your own
  declarative URL connectors or long-running exec plugins with a
  `manifest.json`. See [docs/PLUGINS.md](docs/PLUGINS.md).
- **Full CLI parity** — every v0.3 flag still works, from the same binary.

## Requirements

| | |
|---|---|
| **Required** | Go 1.23+, `gtk4`, `gtk4-layer-shell`, `pkgconf`, `make` |
| **Compositor** | Hyprland (or any `wlr-layer-shell` compositor; banshee falls back to a normal window elsewhere) |
| **Recommended** | `tmux` (session features), `fzf` (nicer CLI picker), `git` (GitHub connector), `wl-clipboard` (calculator copy; `xclip`/`xsel` on X11) |

On Arch:

```sh
sudo pacman -S --needed go gtk4 gtk4-layer-shell pkgconf make tmux fzf git
```

## Install

```sh
git clone https://github.com/jourdanhaines/banshee
cd banshee
./install.sh
```

The installer checks dependencies, builds the binary, runs `make install`, and
then wires banshee into your system:

- `~/.local/bin/banshee`
- shell integration appended to `~/.zshrc` or `~/.bashrc`
- `~/.config/hypr/hyprland.conf`: rebinds `$menu` to `banshee toggle` and
  appends the `layerrule` block — after taking a timestamped backup
- `~/.config/banshee/banshee.conf` and the example plugin, if not already there
- `~/.config/systemd/user/banshee.service` (optional, not enabled)

It is idempotent — re-running it changes nothing that is already in place.
Skip the automatic edits with `--no-hyprland` / `--no-shell`, and undo
everything with `./install.sh --uninstall`.

> The first build compiles the GTK bindings and takes 5–15 minutes. Later
> builds are seconds. `make warm` does just this step.

Prefer to do it by hand? `make install` installs the files and touches nothing
else; then apply [contrib/hyprland.conf](contrib/hyprland.conf) yourself.

Verify with:

```sh
banshee doctor
```

## Hyprland setup

banshee needs one rule block and a `$menu` rebind. `banshee doctor` prints
them, `install.sh` applies them, and
[contrib/hyprland.conf](contrib/hyprland.conf) explains them:

```
layerrule {
    name = banshee
    match:namespace = banshee
    blur = on
    ignore_alpha = 0
}
$menu = banshee toggle
```

The block syntax needs Hyprland >= 0.53. On older releases use the legacy
form instead:

```
layerrule = blur, banshee
layerrule = ignorezero, banshee
```

Most configs already bind `$menu` to a key (`bind = $mainMod, SPACE, exec,
$menu`), so redefining `$menu` is all that is needed to replace wofi or rofi.
If yours has no `$menu`, bind banshee directly:

```
bind = $mainMod, SPACE, exec, banshee toggle
```

The `layerrule` block is what produces the blurred glass. Without it the
panel is opaque but perfectly usable.

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
attached anywhere, that client switches to the session and (under Hyprland)
its terminal window is focused; otherwise a new terminal opens. The alternate
action (`Tab`, `Shift-Enter`, or shift-click) always opens a new terminal
instead. On `Kill <process>` rows the alternate action is SIGKILL instead of
SIGTERM.

## Configuration

`~/.config/banshee/banshee.conf`, `key = value`, one per line. Blank lines and
`#`-comments are ignored, and so are unrecognized keys: a config written for a
newer banshee still loads on an older one. The shipped example is
[contrib/banshee.conf](contrib/banshee.conf).

| Key | Default | Meaning |
|---|---|---|
| `search_paths` | `~/dev,~/projects,~/src` | Comma-separated roots scanned for git repos. `~` is expanded. |
| `max_depth` | `5` | How deep below a search path a `.git` directory may sit. |
| `cache_ttl` | `300` | Seconds the repo list stays cached before a rescan. |
| `keybind` | `ctrl-f` | Key the shell plugin binds to open banshee in the terminal. |
| `fzf_opts` | *(empty)* | Extra arguments for the CLI picker. Split on whitespace — no shell quoting. |
| `startup_prompt` | `true` | Offer to restore the last action when an interactive shell starts. |
| `terminal` | *(auto)* | Terminal used for launcher session rows. Empty auto-detects: `$TERMINAL`, then ghostty, kitty, alacritty, foot. |
| `launcher_width` | `640` | Panel width in pixels. |
| `max_results` | `0` | Maximum rows shown at once. `0` means unlimited — the list scrolls. |
| `accent` | `#7aa2f7` | Accent color (border, caret, icons, selection, badges). |
| `window_opacity` | `0.92` | Panel alpha. The window itself is always transparent. |
| `keyboard_mode` | `exclusive` | `exclusive` or `on-demand` — escape hatch for compositors that mishandle exclusive keyboard focus. |

`banshee reload` applies changes to a running daemon without restarting it.

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
    {
      "name": "edit",
      "panes": [
        { "run": "nvim ." },
        [
          { "run": "npm run dev" },
          { "run": "npm run test:watch" }
        ]
      ]
    },
    {
      "name": "git",
      "panes": [{ "run": "lazygit", "cwd": "~/dev/blacksheep" }]
    }
  ]
}
```

Rules worth knowing:

- **The filename is authoritative.** The tmux session name is the filename with
  `.` and `:` mapped to `_`; the JSON `name` field is informational.
- **`panes` alternates direction by depth.** Depth 0 splits into columns (`-h`),
  a nested array splits the pane it replaces perpendicularly, and so on.
- **`cwd` inherits** session → window → pane, falling back to `$HOME`.
- **Loading is idempotent.** If the session already exists banshee attaches to
  it instead of rebuilding.

Groups are just a list of targets:

```json
{ "v": 1, "name": "dev", "targets": ["blacksheep", "atlas", "banshee"] }
```

`banshee -se <target>` and `banshee -ge <group>` create and edit these for you,
validating on save and re-opening your editor when the JSON is wrong.

## CLI reference

```
banshee [query]         Repo picker → load the target
banshee <target>        Load a target directly (exact match)
banshee -s  <target>    Load target; open $EDITOR to create a config if missing
banshee -se <target>    Edit (or create) a target's session config; no load
banshee -g  <name>      Load a group; prompt to create it if missing
banshee -ge <name>      Edit (or create) a group via multi-select; no load
banshee -r              Re-run the last action
banshee -l              List session configs and groups with live/stopped state
banshee -c              Clear the repository cache
banshee -v              Version
banshee -h              Help

banshee toggle [query]  Show/hide the launcher (starts the daemon if needed)
banshee show   [query]  Show the launcher
banshee hide            Hide the launcher
banshee daemon          Run the launcher daemon in the foreground
banshee reload          Re-read config, repos and plugins
banshee quit            Stop the daemon
banshee doctor          Diagnose the installation
```

Inside tmux, loading a target switches the client. Outside it, banshee hands
the terminal over to `tmux attach`.

Without tmux installed, `banshee [query]` degrades to a fuzzy repo jumper: the
shell plugin picks a repo and `cd`s the current shell into it. That only works
through the plugin — a binary cannot change its parent's working directory —
so source `banshee.plugin.zsh`/`.bash` if you want it.

## Plugins

Two kinds, both a directory under `~/.config/banshee/plugins/<id>/` with a
`manifest.json`:

- **`url` connectors** are declarative. Bind one to a repo with
  `<repo>/.banshee/config.json` and it contributes a row to that repo's block.
  GitHub (binding derived from `git remote get-url origin`) and Railway ship
  built in.
- **`exec` plugins** are long-running child processes speaking newline-delimited
  JSON, optionally gated behind a query prefix. Result actions can open URLs,
  run detached commands, copy text to the clipboard, or call back into the
  plugin.

Full specification, protocol tables and a worked example:
**[docs/PLUGINS.md](docs/PLUGINS.md)**. A runnable sample lives in
[plugins/example/](plugins/example/).

## Running as a systemd service

Optional — `banshee toggle` self-starts the daemon. If you would rather have it
start with your graphical session:

```sh
systemctl --user enable --now banshee.service
```

## Contributing

Architecture, package map and data flow: **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**.
Build, test and review workflow: **[CONTRIBUTING.md](CONTRIBUTING.md)**.

## License

MIT
