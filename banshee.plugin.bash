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
        -h|--help|-v|--version|-r|--restore|-s|--session|-se|--edit|-l|--list|-c|--clear)
            banshee_main "$@"
            return $?
            ;;
        -*)
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
    local line
    while read -r line; do
        [[ "$line" == *"keybind"*"="* ]] || continue
        BANSHEE_KEYBIND="${line#*=}"
        BANSHEE_KEYBIND="${BANSHEE_KEYBIND## }"
        BANSHEE_KEYBIND="${BANSHEE_KEYBIND%% }"
        return
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
    local prev="${COMP_WORDS[COMP_CWORD-1]:-}"

    # After -s/-se, complete session config names
    if [[ "$prev" == "-s" || "$prev" == "--session" || "$prev" == "-se" || "$prev" == "--edit" ]]; then
        local sessions_dir="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/sessions"
        local names=""
        if [[ -d "$sessions_dir" ]]; then
            local f
            for f in "$sessions_dir"/*.json; do
                [[ -e "$f" ]] || continue
                names+="$(basename "$f" .json) "
            done
        fi
        COMPREPLY=($(compgen -W "$names" -- "$cur"))
        return
    fi

    # Handle flag completion
    if [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "--help --version --restore --session --edit --list --clear -r -s -se -l -c" -- "$cur"))
        return
    fi

    # Complete repository names
    local repos
    repos=$(banshee_find_repos 2>/dev/null | while IFS= read -r line; do basename "$line"; done | sort -u)
    COMPREPLY=($(compgen -W "$repos" -- "$cur"))
}

complete -F _banshee_completions banshee

# --- Startup: prompt to restore last loaded session if not all running ---
if [[ $- == *i* ]]; then
    banshee_init 2>/dev/null
    banshee_startup_prompt
fi
