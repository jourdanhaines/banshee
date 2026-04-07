#!/usr/bin/env zsh
# banshee - zsh plugin
# https://github.com/jourdanhaines/banshee

# Resolve the directory this plugin lives in
BANSHEE_PLUGIN_DIR="${0:A:h}"

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

# --- Keybinding widget ---
_banshee_widget() {
    zle -I  # invalidate display
    banshee
    zle reset-prompt
}
zle -N _banshee_widget

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
    local -a repos
    repos=("${(@f)$(banshee_find_repos | while IFS= read -r line; do basename "$line"; done | sort -u)}")
    _describe 'git repositories' repos
}

compdef _banshee_complete banshee

# --- Sync sessions on shell exit (if tmux is available) ---
_banshee_zshexit() {
    command -v tmux &>/dev/null && banshee_sync_sessions 2>/dev/null
}
add-zsh-hook zshexit _banshee_zshexit 2>/dev/null || true
