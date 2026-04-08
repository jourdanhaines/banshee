#!/usr/bin/env bash
# banshee - fluid git repository navigation powered by fzf
# https://github.com/jourdanhaines/banshee

# Only apply strict mode when executed directly, not when sourced
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    set -euo pipefail
fi

BANSHEE_VERSION="0.1.0"
BANSHEE_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/banshee"
BANSHEE_DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/banshee"
BANSHEE_CONFIG_FILE="$BANSHEE_CONFIG_DIR/banshee.conf"
BANSHEE_SESSION_FILE="$BANSHEE_DATA_DIR/sessions"
BANSHEE_CACHE_FILE="$BANSHEE_DATA_DIR/repo_cache"
BANSHEE_STATE_FILE="$BANSHEE_DATA_DIR/session_state"

# --- Defaults (overridable via config) ---
BANSHEE_SEARCH_PATHS=("$HOME")
BANSHEE_MAX_DEPTH=5
BANSHEE_KEYBIND="ctrl-f"
BANSHEE_FZF_OPTS=""
BANSHEE_CACHE_TTL=300  # seconds

# --- Load config ---
banshee_load_config() {
    [[ -f "$BANSHEE_CONFIG_FILE" ]] || return 0
    local line key value
    while read -r line; do
        [[ -z "$line" || "$line" == \#* ]] && continue
        [[ "$line" == *"="* ]] || continue
        key="${line%%=*}"
        value="${line#*=}"
        key="${key## }"; key="${key%% }"
        value="${value## }"; value="${value%% }"
        case "$key" in
            search_paths)
                # Split on comma without touching IFS
                BANSHEE_SEARCH_PATHS=()
                local _remainder="$value"
                while [[ -n "$_remainder" ]]; do
                    local _entry="${_remainder%%,*}"
                    _entry="${_entry## }"; _entry="${_entry%% }"
                    BANSHEE_SEARCH_PATHS+=("$_entry")
                    [[ "$_remainder" == *,* ]] && _remainder="${_remainder#*,}" || _remainder=""
                done
                ;;
            max_depth)    BANSHEE_MAX_DEPTH="$value" ;;
            keybind)      BANSHEE_KEYBIND="$value" ;;
            fzf_opts)     BANSHEE_FZF_OPTS="$value" ;;
            cache_ttl)    BANSHEE_CACHE_TTL="$value" ;;
        esac
    done < "$BANSHEE_CONFIG_FILE"
}

# --- Ensure directories exist ---
banshee_init() {
    [[ -d "$BANSHEE_CONFIG_DIR" ]] || command mkdir -p "$BANSHEE_CONFIG_DIR"
    [[ -d "$BANSHEE_DATA_DIR" ]]   || command mkdir -p "$BANSHEE_DATA_DIR"
    banshee_load_config
}

# --- Find git repositories ---
banshee_find_repos() {
    local use_cache=false

    # Check cache freshness
    if [[ -f "$BANSHEE_CACHE_FILE" ]]; then
        local cache_age
        local file_mtime
        if [[ "$OSTYPE" == darwin* ]]; then
            file_mtime=$(stat -f %m "$BANSHEE_CACHE_FILE" 2>/dev/null || echo 0)
        else
            file_mtime=$(stat -c %Y "$BANSHEE_CACHE_FILE" 2>/dev/null || echo 0)
        fi
        cache_age=$(( $(date +%s) - file_mtime ))
        if (( cache_age < BANSHEE_CACHE_TTL )); then
            use_cache=true
        fi
    fi

    if $use_cache; then
        cat "$BANSHEE_CACHE_FILE"
        return
    fi

    local repos=()
    for search_path in "${BANSHEE_SEARCH_PATHS[@]}"; do
        search_path=$(eval echo "$search_path")  # expand ~
        [[ -d "$search_path" ]] || continue

        if command -v fd &>/dev/null; then
            while IFS= read -r repo; do
                repos+=("$(dirname "$repo")")
            done < <(fd --hidden --no-ignore --type d --max-depth "$BANSHEE_MAX_DEPTH" '^\.git$' "$search_path" 2>/dev/null)
        else
            while IFS= read -r repo; do
                repos+=("$(dirname "$repo")")
            done < <(find "$search_path" -maxdepth "$BANSHEE_MAX_DEPTH" -type d -name ".git" 2>/dev/null)
        fi
    done

    # Deduplicate and sort
    printf '%s\n' "${repos[@]}" | sort -u | tee "$BANSHEE_CACHE_FILE"
}

# --- List repo names (basenames) for completion ---
banshee_list_repo_names() {
    banshee_find_repos | xargs -I{} basename {} | sort -u
}

# --- Select a repo via fzf ---
banshee_select_repo() {
    local query="${1:-}"
    local repos
    repos=$(banshee_find_repos)

    [[ -z "$repos" ]] && echo "banshee: no git repositories found" >&2 && return 1

    # Exact basename match — go directly without fzf
    if [[ -n "$query" ]]; then
        local exact_matches
        exact_matches=$(echo "$repos" | while IFS= read -r r; do
            [[ "$(basename "$r")" == "$query" ]] && echo "$r"
        done)
        if [[ -n "$exact_matches" ]]; then
            local count
            count=$(echo "$exact_matches" | wc -l)
            if (( count == 1 )); then
                echo "$exact_matches"
                return 0
            fi
        fi
    fi

    # Build name->path mapping
    local -A repo_map
    local names=""
    while IFS= read -r repo_path; do
        local name
        name=$(basename "$repo_path")
        repo_map[$name]="$repo_path"
        names+="$name"$'\n'
    done <<< "$repos"

    local preview_cmd='
        name={}
        path=$(echo "$BANSHEE_REPO_LIST" | grep "|${name}$" | head -1 | cut -d"|" -f1)
        printf "\033[1;34m%s\033[0m\n" "$path"
        echo ""
        readme="$path/README.md"
        if [[ -f "$readme" ]]; then
            while IFS= read -r line; do echo "$line"; done < "$readme"
        else
            echo "No preview"
        fi
    '

    # Export repo list as path|name pairs for the preview command
    local repo_list=""
    while IFS= read -r repo_path; do
        [[ -z "$repo_path" ]] && continue
        repo_list+="$repo_path|$(basename "$repo_path")"$'\n'
    done <<< "$repos"

    local fzf_args=(
        --layout=reverse
        --border
        --prompt="banshee> "
        --header="Select a git repository"
        --preview="$preview_cmd"
        --preview-label-pos=0
        --preview-window=right:50%
    )

    [[ -n "$query" ]] && fzf_args+=(--query="$query")
    [[ -n "$BANSHEE_FZF_OPTS" ]] && eval "fzf_args+=($BANSHEE_FZF_OPTS)"

    local selected_name
    selected_name=$(echo "$names" | sed '/^$/d' | BANSHEE_REPO_LIST="$repo_list" fzf "${fzf_args[@]}") || return 1

    # Resolve name back to full path
    echo "${repo_map[$selected_name]}"
}

# --- tmux session management ---
banshee_has_tmux() {
    command -v tmux &>/dev/null
}

banshee_session_name() {
    local repo_path="$1"
    local name
    name=$(basename "$repo_path")
    # tmux doesn't allow dots or colons in session names
    echo "${name//[.:]/_}"
}

banshee_goto_repo() {
    local repo_path="$1"

    if ! banshee_has_tmux; then
        # No tmux: just cd
        echo "$repo_path"
        return 0
    fi

    local session_name
    session_name=$(banshee_session_name "$repo_path")

    # Save session for persistence
    banshee_save_session "$session_name" "$repo_path"

    # Check if we're inside tmux
    if [[ -n "${TMUX:-}" ]]; then
        # Inside tmux
        if tmux has-session -t "=$session_name" 2>/dev/null; then
            tmux switch-client -t "=$session_name"
        else
            tmux new-session -d -s "$session_name" -c "$repo_path"
            tmux switch-client -t "=$session_name"
        fi
    else
        # Outside tmux
        if tmux has-session -t "=$session_name" 2>/dev/null; then
            tmux attach-session -t "=$session_name"
        else
            tmux new-session -s "$session_name" -c "$repo_path"
        fi
    fi
}

# --- Session persistence ---
banshee_save_session() {
    local session_name="$1"
    local repo_path="$2"

    # Remove existing entry for this session, then append
    if [[ -f "$BANSHEE_SESSION_FILE" ]]; then
        local tmp
        tmp=$(grep -v "^${session_name}|" "$BANSHEE_SESSION_FILE" 2>/dev/null || true)
        echo "$tmp" > "$BANSHEE_SESSION_FILE"
    fi
    echo "${session_name}|${repo_path}" >> "$BANSHEE_SESSION_FILE"
}

banshee_remove_session() {
    local session_name="$1"
    [[ -f "$BANSHEE_SESSION_FILE" ]] || return 0
    local tmp
    tmp=$(grep -v "^${session_name}|" "$BANSHEE_SESSION_FILE" 2>/dev/null || true)
    echo "$tmp" > "$BANSHEE_SESSION_FILE"
}

banshee_sync_sessions() {
    banshee_has_tmux || return 0

    # Start with existing saved sessions that are still running
    local synced="" line sname spath
    if [[ -f "$BANSHEE_SESSION_FILE" ]]; then
        local active_sessions
        active_sessions=$(tmux list-sessions -F '#{session_name}' 2>/dev/null || true)
        while read -r line; do
            [[ -z "$line" ]] && continue
            sname="${line%%|*}"
            spath="${line#*|}"
            if echo "$active_sessions" | command grep -qx "$sname"; then
                synced+="${sname}|${spath}"$'\n'
            fi
        done < "$BANSHEE_SESSION_FILE"
    fi

    # Add any running tmux sessions whose working directory is a git repo
    local sess_name sess_path
    while read -r line; do
        [[ -z "$line" ]] && continue
        sess_name="${line%%|*}"
        sess_path="${line#*|}"
        # Skip if already tracked
        [[ "$synced" == *"${sess_name}|"* ]] && continue
        # Only add if it's a git repo
        [[ -d "${sess_path}/.git" ]] || continue
        synced+="${sess_name}|${sess_path}"$'\n'
    done <<< "$(tmux list-sessions -F '#{session_name}|#{session_path}' 2>/dev/null || true)"

    echo "$synced" > "$BANSHEE_SESSION_FILE"
    banshee_save_state
}

# --- Save detailed session state (panes, layouts, directories) ---
banshee_save_state() {
    banshee_has_tmux || return 0
    [[ -f "$BANSHEE_SESSION_FILE" ]] || return 0

    : > "$BANSHEE_STATE_FILE"

    local line sname
    while IFS= read -r line; do
        [[ -z "$line" ]] && continue
        sname="${line%%|*}"
        tmux has-session -t "=$sname" 2>/dev/null || continue
        tmux list-panes -t "=$sname" -s \
            -F '#{session_name}|#{window_index}|#{window_name}|#{pane_index}|#{pane_current_path}|#{pane_active}|#{window_active}|#{window_layout}' \
            >> "$BANSHEE_STATE_FILE" 2>/dev/null || true
    done < "$BANSHEE_SESSION_FILE"
}

# --- Restore sessions with full pane/window state ---
banshee_restore_sessions() {
    banshee_has_tmux || { echo "banshee: tmux is not installed" >&2; return 1; }

    local has_state=false
    [[ -f "$BANSHEE_STATE_FILE" && -s "$BANSHEE_STATE_FILE" ]] && has_state=true

    # Fall back to simple restore if no detailed state
    if ! $has_state; then
        [[ -f "$BANSHEE_SESSION_FILE" ]] || { echo "banshee: no saved sessions to restore" >&2; return 1; }
        local restored=0 line sname spath
        while IFS= read -r line; do
            [[ -z "$line" ]] && continue
            sname="${line%%|*}"
            spath="${line#*|}"
            [[ -d "$spath" ]] || continue
            if ! tmux has-session -t "=$sname" 2>/dev/null; then
                tmux new-session -d -s "$sname" -c "$spath"
                echo "banshee: restored session '$sname' -> $spath"
                ((restored++))
            fi
        done < "$BANSHEE_SESSION_FILE"
        if (( restored == 0 )); then
            echo "banshee: no sessions needed restoring"
        else
            echo "banshee: restored $restored session(s)"
        fi
        return 0
    fi

    # Full state restore
    local restored=0
    local base_idx
    base_idx=$(tmux show-option -gv base-index 2>/dev/null || echo 0)

    # Get unique session names from state file
    local sessions
    sessions=$(awk -F'|' '{print $1}' "$BANSHEE_STATE_FILE" | sort -u)

    local sname
    while IFS= read -r sname; do
        [[ -z "$sname" ]] && continue

        if tmux has-session -t "=$sname" 2>/dev/null; then
            echo "banshee: session '$sname' already running"
            continue
        fi

        local win_num=0 active_win_num="" prev_win_idx=""
        local first_pane=true cur_win_target="" prev_layout=""
        local _rest _sn win_idx win_name pane_idx pane_dir pane_active win_active win_layout

        while IFS= read -r pane_line; do
            [[ -z "$pane_line" ]] && continue
            # Parse pipe-separated fields without IFS mutation
            _rest="$pane_line"
            _sn="${_rest%%|*}"; _rest="${_rest#*|}"
            [[ "$_sn" == "$sname" ]] || continue
            win_idx="${_rest%%|*}"; _rest="${_rest#*|}"
            win_name="${_rest%%|*}"; _rest="${_rest#*|}"
            pane_idx="${_rest%%|*}"; _rest="${_rest#*|}"
            pane_dir="${_rest%%|*}"; _rest="${_rest#*|}"
            pane_active="${_rest%%|*}"; _rest="${_rest#*|}"
            win_active="${_rest%%|*}"; _rest="${_rest#*|}"
            win_layout="$_rest"

            [[ -d "$pane_dir" ]] || pane_dir="$HOME"

            if [[ "$win_idx" != "$prev_win_idx" ]]; then
                # Apply layout to previous window
                if [[ -n "$cur_win_target" && -n "$prev_layout" ]]; then
                    tmux select-layout -t "$cur_win_target" "$prev_layout" 2>/dev/null || true
                fi

                if $first_pane; then
                    tmux new-session -d -s "$sname" -c "$pane_dir"
                    first_pane=false
                else
                    tmux new-window -t "=$sname" -c "$pane_dir"
                fi

                cur_win_target="=$sname:$((base_idx + win_num))"

                if [[ -n "$win_name" && "$win_name" != "zsh" && "$win_name" != "bash" && "$win_name" != "fish" ]]; then
                    tmux rename-window -t "$cur_win_target" "$win_name" 2>/dev/null || true
                fi

                if [[ "$win_active" == "1" ]]; then
                    active_win_num=$((base_idx + win_num))
                fi
                prev_win_idx="$win_idx"
                prev_layout="$win_layout"
                ((win_num++))
            else
                # Additional pane in current window — split
                tmux split-window -t "$cur_win_target" -c "$pane_dir"
                prev_layout="$win_layout"
            fi

            # Select active pane
            if [[ "$pane_active" == "1" && -n "$cur_win_target" ]]; then
                tmux select-pane -t "${cur_win_target}.${pane_idx}" 2>/dev/null || true
            fi
        done < "$BANSHEE_STATE_FILE"

        # Apply layout to last window
        if [[ -n "$cur_win_target" && -n "$prev_layout" ]]; then
            tmux select-layout -t "$cur_win_target" "$prev_layout" 2>/dev/null || true
        fi

        # Select active window
        if [[ -n "$active_win_num" ]]; then
            tmux select-window -t "=$sname:${active_win_num}" 2>/dev/null || true
        fi

        if (( win_num > 0 )); then
            echo "banshee: restored session '$sname' ($win_num window(s))"
            ((restored++))
        fi
    done <<< "$sessions"

    # Also restore sessions from session file that aren't in state file
    if [[ -f "$BANSHEE_SESSION_FILE" ]]; then
        local line spath
        while IFS= read -r line; do
            [[ -z "$line" ]] && continue
            sname="${line%%|*}"
            spath="${line#*|}"
            tmux has-session -t "=$sname" 2>/dev/null && continue
            [[ -d "$spath" ]] || continue
            tmux new-session -d -s "$sname" -c "$spath"
            echo "banshee: restored session '$sname' -> $spath"
            ((restored++))
        done < "$BANSHEE_SESSION_FILE"
    fi

    if (( restored == 0 )); then
        echo "banshee: no sessions needed restoring"
    else
        echo "banshee: restored $restored session(s)"
    fi
}

# --- Clear repo cache ---
banshee_clear_cache() {
    rm -f "$BANSHEE_CACHE_FILE"
    echo "banshee: cache cleared"
}

# --- Usage ---
banshee_usage() {
    cat <<'EOF'
banshee - fluid git repository navigation powered by fzf

Usage:
  banshee [query]         Find and navigate to a git repository
  banshee -r, --restore   Restore saved tmux sessions (panes, layouts, directories)
  banshee -s, --sync      Sync and save session state (panes, layouts, directories)
  banshee -l, --list      List saved sessions
  banshee -c, --clear     Clear the repository cache
  banshee -v, --version   Show version
  banshee -h, --help      Show this help

Configuration: ~/.config/banshee/banshee.conf

EOF
}

# --- Main entry point ---
banshee_main() {
    banshee_init

    case "${1:-}" in
        -h|--help)
            banshee_usage
            return 0
            ;;
        -v|--version)
            echo "banshee $BANSHEE_VERSION"
            return 0
            ;;
        -r|--restore)
            banshee_restore_sessions
            return $?
            ;;
        -s|--sync)
            banshee_sync_sessions
            echo "banshee: sessions synced"
            return 0
            ;;
        -l|--list)
            if [[ -f "$BANSHEE_SESSION_FILE" ]]; then
                local line sname spath
                while read -r line; do
                    [[ -z "$line" ]] && continue
                    sname="${line%%|*}"
                    spath="${line#*|}"
                    local state="stopped"
                    if banshee_has_tmux && tmux has-session -t "=$sname" 2>/dev/null; then
                        state="running"
                    fi
                    printf "  %-20s %s [%s]\n" "$sname" "$spath" "$state"
                done < "$BANSHEE_SESSION_FILE"
            else
                echo "banshee: no saved sessions"
            fi
            return 0
            ;;
        -c|--clear)
            banshee_clear_cache
            return 0
            ;;
        -*)
            echo "banshee: unknown option '$1'" >&2
            banshee_usage >&2
            return 1
            ;;
        *)
            # Select repo with optional query
            local selected
            selected=$(banshee_select_repo "${1:-}") || return 1

            if banshee_has_tmux; then
                banshee_goto_repo "$selected"
            else
                # No tmux — output path for cd (called via shell function wrapper)
                echo "$selected"
            fi
            ;;
    esac
}

# Only run main if executed directly (not sourced)
# BASH_SOURCE is bash-only; ZSH_EVAL_CONTEXT is zsh-only
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    banshee_main "$@"
fi
