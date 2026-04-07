#!/usr/bin/env bash
# banshee - bash plugin
# https://github.com/jourdanhaines/banshee

# Resolve the directory this plugin lives in
BANSHEE_PLUGIN_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source the core script
source "$BANSHEE_PLUGIN_DIR/banshee.sh"

# --- Shell wrapper (so cd works in the current shell) ---
banshee() {
    banshee_init

    case "${1:-}" in
        -h|--help|-v|--version|-r|--restore|-s|--sync|-l|--list|-c|--clear)
            banshee_main "$@"
            return $?
            ;;
        *)
            local selected
            selected=$(banshee_select_repo "${1:-}") || return 0

            if banshee_has_tmux; then
                banshee_goto_repo "$selected"
            else
                cd "$selected" || return 1
            fi
            ;;
    esac
}

# --- Keybinding ---
_banshee_keybind() {
    banshee
}

# Read keybind from config file directly (no init, no subcommands)
_banshee_read_keybind() {
    local conf="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/banshee.conf"
    [[ -f "$conf" ]] || return
    while IFS='=' read -r key value; do
        key="${key## }"; key="${key%% }"
        value="${value## }"; value="${value%% }"
        [[ "$key" == "keybind" ]] && BANSHEE_KEYBIND="$value" && return
    done < "$conf"
}
_banshee_read_keybind

# Bind the key (configurable via banshee.conf)
_banshee_bind_key() {
    local key_seq
    case "${BANSHEE_KEYBIND:-ctrl-f}" in
        ctrl-f)  key_seq="\C-f" ;;
        ctrl-g)  key_seq="\C-g" ;;
        ctrl-b)  key_seq="\C-b" ;;
        ctrl-p)  key_seq="\C-p" ;;
        ctrl-o)  key_seq="\C-o" ;;
        ctrl-\\) key_seq="\C-\\" ;;
        *)       key_seq="$BANSHEE_KEYBIND" ;;
    esac

    # Only bind if the terminal supports it (interactive shell with readline)
    if [[ $- == *i* ]] && [[ -t 0 ]]; then
        bind -x "\"$key_seq\": _banshee_keybind" 2>/dev/null || true
    fi
}

_banshee_bind_key

# --- Tab completion ---
_banshee_completions() {
    local cur="${COMP_WORDS[COMP_CWORD]}"

    # Handle flag completion
    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "--help --version --restore --sync --list --clear" -- "$cur"))
        return
    fi

    # Complete repository names
    local repos
    repos=$(banshee_find_repos 2>/dev/null | while IFS= read -r line; do basename "$line"; done | sort -u)
    COMPREPLY=($(compgen -W "$repos" -- "$cur"))
}

complete -F _banshee_completions banshee

# --- Sync sessions on shell exit (if tmux is available) ---
if command -v tmux &>/dev/null; then
    _banshee_bash_exit() {
        banshee_sync_sessions 2>/dev/null
    }
    trap _banshee_bash_exit EXIT
fi
