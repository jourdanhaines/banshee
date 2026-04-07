# banshee

Fast git repository switching for the terminal, powered by [fzf](https://github.com/junegunn/fzf).

Banshee scans your filesystem for git repos, lets you fuzzy-find one, and drops you into it. If tmux is installed, it manages named sessions per repo automatically.

## Dependencies

**Required:** `fzf`, `git`
**Optional:** `tmux` (session management), `fd` (faster repo scanning)

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
banshee              # launch fzf repo picker
banshee myproject    # launch with pre-filled query
banshee -r           # restore saved tmux sessions
banshee -s           # sync saved sessions (prune closed ones)
banshee -l           # list saved sessions
banshee -c           # clear repo cache
```

**Ctrl+F** launches banshee inline (configurable).

Tab completion works out of the box — type `banshee <tab>` to complete repo names.

## tmux integration

When tmux is available, selecting a repo will:

1. Switch to the existing tmux session if one is already open for that repo
2. Create a new session named after the repo otherwise

Sessions are persisted to disk. Use `banshee -r` after a reboot to restore them all.

## Configuration

`~/.config/banshee/banshee.conf`

```sh
search_paths = ~/dev,~/projects,~/src   # where to scan
max_depth = 5                           # how deep to look
keybind = ctrl-f                        # inline launch key
cache_ttl = 300                         # repo cache lifetime (seconds)
fzf_opts =                              # extra fzf flags
```

## Uninstall

```sh
cd banshee
./install.sh --uninstall
```

Remove the `source` line from your shell config.
