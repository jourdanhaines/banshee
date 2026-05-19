# banshee

Fast git repository switching and declarative tmux sessions for the terminal, powered by [fzf](https://github.com/junegunn/fzf).

Banshee fuzzy-finds git repos, drops you into them, and — when a target has a session config — auto-loads its declared tmux layout (windows, splits, commands). Groups bundle multiple targets into a single launch.

## Dependencies

**Required:** `fzf`, `git`, `jq`
**Optional:** `tmux` (sessions/groups), `fd` (faster repo scanning)

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/jourdanhaines/banshee/main/install.sh | bash
```

Then add to your shell config:

```sh
# ~/.zshrc
source "$HOME/.local/share/banshee/plugin/banshee.plugin.zsh"

# ~/.bashrc
source "$HOME/.local/share/banshee/plugin/banshee.plugin.bash"
```

Ensure `~/.local/bin` is in your `PATH`.

## Usage

| Command | What it does |
|---------|--------------|
| `banshee` | fzf repo picker → load the picked target |
| `banshee <target>` | Load `<target>` (config-driven if defined, otherwise plain session at repo path) |
| `banshee -s <target>` | Load `<target>`; if no config exists, open `$EDITOR` to create one first |
| `banshee -se <target>` | Edit (or create) the `<target>` session config; **no load** |
| `banshee -g <name>` | Load group `<name>`; if missing, fzf multi-select prompts to create it |
| `banshee -ge <name>` | Edit (or create) the group via multi-select; **no load** |
| `banshee -r` | Re-run the last action (target or group) |
| `banshee -l` | List session configs and groups, with running state |
| `banshee -c` | Clear the repository cache |

**Ctrl+F** opens the picker inline (configurable in `banshee.conf`).

Tab completion:
- `banshee <tab>` — repo basenames
- `banshee -s <tab>` / `banshee -se <tab>` — existing target configs
- `banshee -g <tab>` / `banshee -ge <tab>` — existing groups

## Target session configs

A target is a name (typically a repo basename). When `banshee <target>` runs, banshee looks for `~/.config/banshee/sessions/<target>.json` — if found, the tmux session is constructed from that config. Otherwise a plain session opens at the repo path.

```json
{
  "v": 1,
  "name": "blacksheep",
  "cwd": "~/dev/blacksheep",
  "windows": [
    {
      "name": "neovim",
      "panes": [ { "run": "nvim" } ]
    },
    {
      "name": "dev",
      "panes": [
        { "run": "bun nx dev app" },
        [
          { "run": "bun nx serve server" },
          { "run": "bun nx serve worker" }
        ]
      ]
    }
  ]
}
```

### Field reference

| Field | Required | Notes |
|-------|----------|-------|
| `v` | yes | Schema version. Only `1` supported. |
| `name` | yes | Informational. tmux session name comes from the filename. |
| `cwd` | no | Default working dir for windows/panes. `~` expanded. Falls back to the matching repo path, then `$HOME`. |
| `windows[]` | yes | tmux windows, in order. |
| `windows[].name` | no | tmux window name. |
| `windows[].cwd` | no | Overrides session `cwd`. |
| `windows[].panes` | yes | Recursive layout tree. |
| `panes[i]` | yes | Either `{ "run": "<cmd>", "cwd": "<optional>" }` or a nested array for sub-splits. |

### Pane layout tree

`panes` is an array. **Depth alternates split direction**:
- Top level (depth 0): rows, top → bottom.
- One level down (depth 1): columns, side by side.
- Deeper: alternating perpendicular splits.

The `dev` window above lays out as:

```
+----------------------+
|  bun nx dev app      |
+----------+-----------+
|  server  |  worker   |
+----------+-----------+
```

## Groups

A group is a saved multi-select of targets — useful for "launch my whole work setup at once".

```sh
banshee -g work
```

On first invocation, an fzf multi-select prompt appears with all known targets (repo basenames ∪ existing target configs). TAB to mark, ENTER to confirm. The selections are saved to `~/.config/banshee/groups/work.json`:

```json
{
  "v": 1,
  "name": "work",
  "targets": ["banshee", "blacksheep", "dotfiles"]
}
```

Subsequent runs of `banshee -g work` create a tmux session for each target (config-driven if defined, plain otherwise) and attach to the first one in the list.

```sh
banshee -ge work
```

Re-runs the multi-select prompt — current selections are listed in the header and floated to the top of the pool for quick re-ticking. Saves without launching.

## Last action / `-r`

Every `banshee <target>`, `banshee -s <target>`, and `banshee -g <name>` records itself to `~/.local/share/banshee/last_action`. `banshee -r` replays whichever was most recent. Useful after reboot or `tmux kill-server`.

`-se`, `-ge`, `-l`, `-c` do **not** update the last action.

## Configuration

`~/.config/banshee/banshee.conf`

```sh
search_paths = ~/dev,~/projects,~/src   # where to scan for repos
max_depth = 5                           # how deep to look
keybind = ctrl-f                        # inline launch key
cache_ttl = 300                         # repo cache lifetime (seconds)
fzf_opts =                              # extra fzf flags
startup_prompt = true                   # offer to restore last action on shell start
```

## Uninstall

```sh
cd banshee
./install.sh --uninstall
```

Remove the `source` line from your shell config.
