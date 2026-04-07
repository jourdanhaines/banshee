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
            selected=$(banshee_select_repo "${1:-}") || return 1

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

# Load config to get keybind
banshee_init

# Bind the key (configurable via banshee.conf)
bindkey "^f" _banshee_widget 2>/dev/null  # default fallback
case "$BANSHEE_KEYBIND" in
    ctrl-f)  bindkey "^f" _banshee_widget ;;
    ctrl-g)  bindkey "^g" _banshee_widget ;;
    ctrl-b)  bindkey "^b" _banshee_widget ;;
    ctrl-p)  bindkey "^p" _banshee_widget ;;
    ctrl-o)  bindkey "^o" _banshee_widget ;;
    ctrl-\\) bindkey "^\\" _banshee_widget ;;
    *)
        # Attempt to bind raw sequence if it looks like a key notation
        bindkey "$BANSHEE_KEYBIND" _banshee_widget 2>/dev/null || true
        ;;
esac

# --- Tab completion ---
_banshee_complete() {
    local -a repos
    repos=("${(@f)$(banshee_find_repos | while IFS= read -r line; do basename "$line"; done | sort -u)}")

    # If only one match, complete it directly
    # If multiple matches or partial input, offer completion list
    _describe 'git repositories' repos
}

compdef _banshee_complete banshee

# --- Sync sessions on shell exit (if tmux is available) ---
if banshee_has_tmux; then
    _banshee_zshexit() {
        banshee_sync_sessions 2>/dev/null
    }
    add-zsh-hook zshexit _banshee_zshexit 2>/dev/null || true
fi
