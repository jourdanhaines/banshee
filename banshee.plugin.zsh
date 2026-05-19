#!/usr/bin/env zsh
# banshee - zsh plugin
# https://github.com/jourdanhaines/banshee

# Resolve the directory this plugin lives in
BANSHEE_PLUGIN_DIR="${0:A:h}"

# Source the core script
source "$BANSHEE_PLUGIN_DIR/banshee.sh"

# --- Shell wrapper (so cd works in the current shell when no tmux) ---
banshee() {
    banshee_init

    case "${1:-}" in
        -h|--help|-v|--version|-r|--restore|-l|--list|-c|--clear|-s|--session|-se|--edit-session|-g|--group|-ge|--edit-group)
            banshee_main "$@"
            return $?
            ;;
        -*)
            banshee_main "$@"
            return $?
            ;;
        *)
            if banshee_has_tmux; then
                banshee_main "$@"
                return $?
            fi
            local selected
            selected=$(banshee_select_repo "${1:-}") || return 0
            cd "$selected" || return 1
            ;;
    esac
}

# --- Keybinding widget ---
_banshee_widget() {
    zle -I  # invalidate display
    banshee
    zle reset-prompt
}
zle -N _banshee_widget

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

case "${BANSHEE_KEYBIND:-ctrl-f}" in
    ctrl-f)  bindkey "^f" _banshee_widget ;;
    ctrl-g)  bindkey "^g" _banshee_widget ;;
    ctrl-b)  bindkey "^b" _banshee_widget ;;
    ctrl-p)  bindkey "^p" _banshee_widget ;;
    ctrl-o)  bindkey "^o" _banshee_widget ;;
    ctrl-\\) bindkey "^\\" _banshee_widget ;;
    *)       bindkey "$BANSHEE_KEYBIND" _banshee_widget 2>/dev/null || true ;;
esac

# --- Tab completion ---
_banshee_complete() {
    local prev="${words[CURRENT-1]:-}"
    local sessions_dir="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/sessions"
    local groups_dir="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/groups"

    case "$prev" in
        -s|--session|-se|--edit-session)
            local -a names
            names=()
            if [[ -d "$sessions_dir" ]]; then
                local f
                for f in "$sessions_dir"/*.json(N); do
                    names+=("${f:t:r}")
                done
            fi
            _describe 'target configs' names
            return
            ;;
        -g|--group|-ge|--edit-group)
            local -a names
            names=()
            if [[ -d "$groups_dir" ]]; then
                local f
                for f in "$groups_dir"/*.json(N); do
                    names+=("${f:t:r}")
                done
            fi
            _describe 'groups' names
            return
            ;;
    esac

    local -a repos
    repos=("${(@f)$(banshee_find_repos | while IFS= read -r line; do basename "$line"; done | sort -u)}")
    _describe 'git repositories' repos
}

compdef _banshee_complete banshee

# --- Startup: prompt to restore last action if not running ---
if [[ -o interactive ]]; then
    banshee_init 2>/dev/null
    banshee_startup_prompt
fi
