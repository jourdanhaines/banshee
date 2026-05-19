# banshee

Fast git repository switching and declarative tmux sessions for the terminal, powered by [fzf](https://github.com/junegunn/fzf).

Banshee fuzzy-finds git repos and drops you into them. When tmux is installed, it also lets you define rich session presets in JSON — windows, splits, and per-pane commands.

## Dependencies

**Required:** `fzf`, `git`, `jq`
**Optional:** `tmux` (sessions), `fd` (faster repo scanning)

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

```sh
banshee              # fzf repo picker
banshee myproject    # fzf with pre-filled query
banshee -s <name>    # load session config (open editor if it doesn't exist)
banshee -se <name>   # edit existing session config (no auto-load)
banshee -r           # restore last loaded session
banshee -l           # list session configs and their tmux state
banshee -c           # clear repo cache
```

**Ctrl+F** launches the repo picker inline (configurable).

Tab completion: `banshee <tab>` completes repo names; `banshee -s <tab>` completes session config names.

## Session configs

Each session config is a JSON file at `~/.config/banshee/sessions/<name>.json`. A single file can define multiple tmux sessions (a "bundle"), each with its own windows and panes.

```json
{
  "v": 1,
  "sessions": [
    {
      "name": "projectA",
      "cwd": "~/dev/projectA",
      "windows": [
        {
          "name": "neovim",
          "panes": [
            { "run": "nvim" }
          ]
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
  ]
}
```

### Field reference

| Field | Required | Notes |
|-------|----------|-------|
| `v` | yes | Schema version. Only `1` supported. |
| `sessions[]` | yes | One entry per tmux session to create. |
| `sessions[].name` | yes | tmux session name (`.` / `:` → `_`). |
| `sessions[].cwd` | no | Default working dir. `~` expanded. Falls back to `$HOME`. |
| `sessions[].windows[]` | yes | tmux windows, in order. |
| `windows[].name` | no | tmux window name. |
| `windows[].cwd` | no | Overrides session `cwd`. |
| `windows[].panes` | yes | Recursive layout tree. |
| `panes[i]` | yes | Either `{ "run": "<cmd>", "cwd": "<optional>" }` or a nested array. |

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

### First-time flow

```sh
banshee -s work       # file doesn't exist → editor opens with default template
                      # save valid JSON → bundle gets loaded
banshee -s work       # subsequent runs → load immediately
banshee -se work      # edit without loading
banshee -r            # rebuild last-loaded bundle (e.g. after reboot)
```

The last `-s <name>` invocation is remembered in `~/.local/share/banshee/last_loaded`. `banshee -r` reads that and re-runs the load.

## Configuration

`~/.config/banshee/banshee.conf`

```sh
search_paths = ~/dev,~/projects,~/src   # where to scan for repos
max_depth = 5                           # how deep to look
keybind = ctrl-f                        # inline launch key
cache_ttl = 300                         # repo cache lifetime (seconds)
fzf_opts =                              # extra fzf flags
startup_prompt = true                   # offer to restore last session on shell start
```

## Uninstall

```sh
cd banshee
./install.sh --uninstall
```

Remove the `source` line from your shell config.
