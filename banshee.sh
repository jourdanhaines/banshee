#!/usr/bin/env bash
# banshee - fluid git repository navigation powered by fzf
# https://github.com/jourdanhaines/banshee

# Only apply strict mode when executed directly, not when sourced
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    set -euo pipefail
fi

BANSHEE_VERSION="0.2.0"
BANSHEE_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/banshee"
BANSHEE_DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/banshee"
BANSHEE_CONFIG_FILE="$BANSHEE_CONFIG_DIR/banshee.conf"
BANSHEE_SESSIONS_DIR="$BANSHEE_CONFIG_DIR/sessions"
BANSHEE_CACHE_FILE="$BANSHEE_DATA_DIR/repo_cache"
BANSHEE_LAST_FILE="$BANSHEE_DATA_DIR/last_loaded"

# --- Defaults (overridable via config) ---
BANSHEE_SEARCH_PATHS=("$HOME")
BANSHEE_MAX_DEPTH=5
BANSHEE_KEYBIND="ctrl-f"
BANSHEE_FZF_OPTS=""
BANSHEE_CACHE_TTL=300  # seconds
BANSHEE_STARTUP_PROMPT=true

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
                BANSHEE_SEARCH_PATHS=()
                local _remainder="$value"
                while [[ -n "$_remainder" ]]; do
                    local _entry="${_remainder%%,*}"
                    _entry="${_entry## }"; _entry="${_entry%% }"
                    BANSHEE_SEARCH_PATHS+=("$_entry")
                    [[ "$_remainder" == *,* ]] && _remainder="${_remainder#*,}" || _remainder=""
                done
                ;;
            max_depth)      BANSHEE_MAX_DEPTH="$value" ;;
            keybind)        BANSHEE_KEYBIND="$value" ;;
            fzf_opts)       BANSHEE_FZF_OPTS="$value" ;;
            cache_ttl)      BANSHEE_CACHE_TTL="$value" ;;
            startup_prompt) BANSHEE_STARTUP_PROMPT="$value" ;;
        esac
    done < "$BANSHEE_CONFIG_FILE"
}

# --- Ensure directories exist + migrate from old session storage ---
banshee_init() {
    [[ -d "$BANSHEE_CONFIG_DIR" ]]   || command mkdir -p "$BANSHEE_CONFIG_DIR"
    [[ -d "$BANSHEE_SESSIONS_DIR" ]] || command mkdir -p "$BANSHEE_SESSIONS_DIR"
    [[ -d "$BANSHEE_DATA_DIR" ]]     || command mkdir -p "$BANSHEE_DATA_DIR"

    # Migration: drop the old flat session/state files (no useful mapping to JSON model)
    [[ -f "$BANSHEE_DATA_DIR/sessions" ]] && rm -f "$BANSHEE_DATA_DIR/sessions" 2>/dev/null
    [[ -f "$BANSHEE_DATA_DIR/session_state" ]] && rm -f "$BANSHEE_DATA_DIR/session_state" 2>/dev/null

    banshee_load_config
}

# --- Find git repositories ---
banshee_find_repos() {
    local use_cache=false

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
        search_path=$(eval echo "$search_path")
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

    echo "${repo_map[$selected_name]}"
}

# --- tmux helpers ---
banshee_has_tmux() {
    command -v tmux &>/dev/null
}

banshee_session_name() {
    local raw="$1"
    # If it's a path, strip to basename
    [[ "$raw" == */* ]] && raw=$(basename "$raw")
    # tmux disallows '.' and ':' in session names
    echo "${raw//[.:]/_}"
}

banshee_goto_repo() {
    local repo_path="$1"

    if ! banshee_has_tmux; then
        echo "$repo_path"
        return 0
    fi

    local session_name
    session_name=$(banshee_session_name "$repo_path")

    if [[ -n "${TMUX:-}" ]]; then
        if tmux has-session -t "=$session_name" 2>/dev/null; then
            tmux switch-client -t "=$session_name"
        else
            tmux new-session -d -s "$session_name" -c "$repo_path"
            tmux switch-client -t "=$session_name"
        fi
    else
        if tmux has-session -t "=$session_name" 2>/dev/null; then
            tmux attach-session -t "=$session_name"
        else
            tmux new-session -s "$session_name" -c "$repo_path"
        fi
    fi
}

# --- Repo cache ---
banshee_clear_cache() {
    rm -f "$BANSHEE_CACHE_FILE"
    echo "banshee: cache cleared"
}

# =============================================================================
# Session config subsystem (JSON-driven)
# =============================================================================

banshee_require_jq() {
    if ! command -v jq &>/dev/null; then
        echo "banshee: jq is required for session management" >&2
        echo "  install it with your package manager (e.g. 'sudo pacman -S jq', 'brew install jq', 'sudo apt install jq')" >&2
        return 1
    fi
}

banshee_expand_path() {
    local p="$1"
    [[ "$p" == "~" ]] && { echo "$HOME"; return; }
    [[ "$p" == "~/"* ]] && p="$HOME/${p:2}"
    echo "$p"
}

banshee_resolve_editor() {
    local ed="${EDITOR:-${VISUAL:-}}"
    if [[ -z "$ed" ]]; then
        local cand
        for cand in nvim vim nano vi; do
            if command -v "$cand" &>/dev/null; then
                ed="$cand"
                break
            fi
        done
    fi
    echo "$ed"
}

banshee_write_default_template() {
    local cfg="$1" name="$2"
    cat > "$cfg" <<EOF
{
  "v": 1,
  "sessions": [
    {
      "name": "$name",
      "windows": [
        {
          "name": "<window_name>",
          "panes": [
            { "run": "<target_command>" }
          ]
        }
      ]
    }
  ]
}
EOF
}

banshee_validate_config() {
    local cfg="$1"
    if ! jq empty "$cfg" 2>/dev/null; then
        echo "banshee: invalid JSON in $cfg" >&2
        return 1
    fi
    if ! jq -e '.v == 1' "$cfg" &>/dev/null; then
        echo "banshee: $cfg missing or unsupported \"v\" (must be 1)" >&2
        return 1
    fi
    if ! jq -e '.sessions | type == "array" and length > 0' "$cfg" &>/dev/null; then
        echo "banshee: $cfg .sessions must be a non-empty array" >&2
        return 1
    fi
    if ! jq -e '.sessions | all(has("name") and (.name | type == "string" and length > 0))' "$cfg" &>/dev/null; then
        echo "banshee: $cfg each session needs a non-empty \"name\"" >&2
        return 1
    fi
    if ! jq -e '.sessions | all(.windows | type == "array" and length > 0)' "$cfg" &>/dev/null; then
        echo "banshee: $cfg each session needs non-empty \"windows\" array" >&2
        return 1
    fi
    if ! jq -e '.sessions | all(.windows | all(.panes | type == "array" and length > 0))' "$cfg" &>/dev/null; then
        echo "banshee: $cfg each window needs non-empty \"panes\" array" >&2
        return 1
    fi
    return 0
}

banshee_session_config_path() {
    echo "$BANSHEE_SESSIONS_DIR/${1}.json"
}

# Open editor on config; loop until valid JSON or user cancels.
# Args: <name> <mode>   mode = "load" | "edit_only"
banshee_edit_session() {
    local name="$1" mode="$2"
    banshee_require_jq || return 1

    [[ -d "$BANSHEE_SESSIONS_DIR" ]] || command mkdir -p "$BANSHEE_SESSIONS_DIR"

    local cfg
    cfg=$(banshee_session_config_path "$name")

    if [[ ! -f "$cfg" ]]; then
        if [[ "$mode" == "edit_only" ]]; then
            echo "banshee: session config '$name' does not exist ($cfg)" >&2
            return 1
        fi
        # -s <name>: file missing → write default + open editor
        banshee_write_default_template "$cfg" "$name"

        local ed
        ed=$(banshee_resolve_editor)
        [[ -z "$ed" ]] && { echo "banshee: no editor found (set \$EDITOR)" >&2; return 1; }

        while true; do
            "$ed" "$cfg"
            if banshee_validate_config "$cfg"; then
                break
            fi
            printf "banshee: config invalid. [r]eopen editor / [c]ancel? "
            local reply=""
            read -r reply || return 1
            case "$reply" in
                ""|r|R) continue ;;
                *) return 1 ;;
            esac
        done

        banshee_load_session "$name"
        return $?
    fi

    if [[ "$mode" == "edit_only" ]]; then
        local ed
        ed=$(banshee_resolve_editor)
        [[ -z "$ed" ]] && { echo "banshee: no editor found (set \$EDITOR)" >&2; return 1; }

        while true; do
            "$ed" "$cfg"
            if banshee_validate_config "$cfg"; then
                break
            fi
            printf "banshee: config invalid. [r]eopen editor / [c]ancel? "
            local reply=""
            read -r reply || return 1
            case "$reply" in
                ""|r|R) continue ;;
                *) return 1 ;;
            esac
        done
        return 0
    fi

    # -s <name> with existing file: just load
    banshee_load_session "$name"
}

# Recursive pane layout walker.
# Args: <target_pane_id> <panes_json> <base_cwd> <depth>
# depth 0 → vertical splits (rows); depth 1 → horizontal (cols); alternating.
# Uses newline-separated pane-id string instead of arrays for bash/zsh portability.
banshee_build_panes() {
    local target_pane="$1" panes_json="$2" base_cwd="$3" depth="$4"

    local len
    len=$(printf '%s' "$panes_json" | jq 'length')
    (( len == 0 )) && return 0

    local dir
    if (( depth % 2 == 0 )); then dir="-v"; else dir="-h"; fi

    # Pass 1: create sibling panes by splitting the previously-created sibling.
    # Splitting the *just-created* sibling keeps panes in JSON order.
    local pane_ids="$target_pane"
    local prev_pane="$target_pane"

    local i percentage new_pane
    for ((i=1; i<len; i++)); do
        # Carrier keeps 1/(len-i+1) share; new pane takes the rest.
        percentage=$((100 - 100 / (len - i + 1)))
        (( percentage < 1 )) && percentage=1
        (( percentage > 99 )) && percentage=99
        new_pane=$(tmux split-window -P -F '#{pane_id}' "$dir" -l "${percentage}%" -t "$prev_pane" -c "$base_cwd" 2>/dev/null) || {
            echo "banshee: split-window failed (depth=$depth, i=$i)" >&2
            return 1
        }
        pane_ids="$pane_ids"$'\n'"$new_pane"
        prev_pane="$new_pane"
    done

    # Pass 2: assign content (commands or recursion) to each sibling.
    # Two-pass avoids interleaving outer splits with inner recursion.
    local element is_array run pane_cwd cur_pane
    i=0
    while IFS= read -r cur_pane; do
        [[ -z "$cur_pane" ]] && { i=$((i+1)); continue; }
        element=$(printf '%s' "$panes_json" | jq -c ".[$i]")
        is_array=$(printf '%s' "$element" | jq -r 'type == "array"')

        if [[ "$is_array" == "true" ]]; then
            banshee_build_panes "$cur_pane" "$element" "$base_cwd" $((depth + 1)) || return 1
        else
            run=$(printf '%s' "$element" | jq -r '.run // ""')
            pane_cwd=$(printf '%s' "$element" | jq -r '.cwd // ""')
            if [[ -n "$pane_cwd" ]]; then
                pane_cwd=$(banshee_expand_path "$pane_cwd")
                if [[ -d "$pane_cwd" ]]; then
                    tmux send-keys -t "$cur_pane" -l "cd $pane_cwd"
                    tmux send-keys -t "$cur_pane" Enter
                fi
            fi
            if [[ -n "$run" ]]; then
                tmux send-keys -t "$cur_pane" -l "$run"
                tmux send-keys -t "$cur_pane" Enter
            fi
        fi
        i=$((i+1))
    done <<< "$pane_ids"
}

# Build one tmux session from a sessions[i] JSON blob.
banshee_build_tmux_session() {
    local session_json="$1"

    local sname scwd
    sname=$(printf '%s' "$session_json" | jq -r '.name')
    sname=$(banshee_session_name "$sname")
    scwd=$(printf '%s' "$session_json" | jq -r '.cwd // ""')
    [[ -z "$scwd" ]] && scwd="$HOME"
    scwd=$(banshee_expand_path "$scwd")
    [[ -d "$scwd" ]] || scwd="$HOME"

    if tmux has-session -t "=$sname" 2>/dev/null; then
        echo "banshee: session '$sname' already running — skipping"
        echo "$sname"  # still emit so caller can target it
        return 0
    fi

    local win_count
    win_count=$(printf '%s' "$session_json" | jq '.windows | length')

    local w wjson wname wcwd panes_json first_pane
    for ((w=0; w<win_count; w++)); do
        wjson=$(printf '%s' "$session_json" | jq -c ".windows[$w]")
        wname=$(printf '%s' "$wjson" | jq -r '.name // ""')
        wcwd=$(printf '%s' "$wjson" | jq -r '.cwd // ""')
        [[ -z "$wcwd" ]] && wcwd="$scwd"
        wcwd=$(banshee_expand_path "$wcwd")
        [[ -d "$wcwd" ]] || wcwd="$scwd"
        panes_json=$(printf '%s' "$wjson" | jq -c '.panes')

        if (( w == 0 )); then
            if [[ -n "$wname" ]]; then
                tmux new-session -d -s "$sname" -n "$wname" -c "$wcwd"
            else
                tmux new-session -d -s "$sname" -c "$wcwd"
            fi
        else
            if [[ -n "$wname" ]]; then
                tmux new-window -d -t "=$sname:" -n "$wname" -c "$wcwd"
            else
                tmux new-window -d -t "=$sname:" -c "$wcwd"
            fi
        fi

        first_pane=$(tmux display-message -p -t "=$sname:{end}" '#{pane_id}' 2>/dev/null)
        [[ -z "$first_pane" ]] && { echo "banshee: failed to resolve first pane id for $sname" >&2; return 1; }

        banshee_build_panes "$first_pane" "$panes_json" "$wcwd" 0 || return 1
    done

    tmux select-window -t "=$sname:^" 2>/dev/null || true
    echo "banshee: created session '$sname'" >&2
    echo "$sname"
}

banshee_load_session() {
    local name="$1"
    banshee_require_jq || return 1
    banshee_has_tmux || { echo "banshee: tmux is not installed" >&2; return 1; }

    local cfg
    cfg=$(banshee_session_config_path "$name")
    [[ -f "$cfg" ]] || { echo "banshee: no session config '$name'" >&2; return 1; }
    banshee_validate_config "$cfg" || return 1

    local count i session_json first_session="" built
    count=$(jq '.sessions | length' "$cfg")
    for ((i=0; i<count; i++)); do
        session_json=$(jq -c ".sessions[$i]" "$cfg")
        built=$(banshee_build_tmux_session "$session_json") || return 1
        [[ -z "$first_session" && -n "$built" ]] && first_session="$built"
    done

    # Atomic record of last loaded bundle name
    local tmp="${BANSHEE_LAST_FILE}.tmp.$$"
    printf '%s\n' "$name" > "$tmp"
    mv -f "$tmp" "$BANSHEE_LAST_FILE"

    [[ -z "$first_session" ]] && return 0

    if [[ -n "${TMUX:-}" ]]; then
        tmux switch-client -t "=$first_session" 2>/dev/null || true
    else
        tmux attach-session -t "=$first_session"
    fi
}

banshee_restore_last() {
    [[ -f "$BANSHEE_LAST_FILE" ]] || { echo "banshee: no previously loaded session" >&2; return 1; }
    local name
    name=$(head -n1 "$BANSHEE_LAST_FILE" 2>/dev/null | tr -d '\r\n')
    [[ -z "$name" ]] && { echo "banshee: no previously loaded session" >&2; return 1; }
    banshee_load_session "$name"
}

banshee_list_sessions() {
    if [[ ! -d "$BANSHEE_SESSIONS_DIR" ]]; then
        echo "banshee: no session configs"
        return 0
    fi
    shopt -s nullglob 2>/dev/null || true
    local files=("$BANSHEE_SESSIONS_DIR"/*.json)
    shopt -u nullglob 2>/dev/null || true
    if (( ${#files[@]} == 0 )); then
        echo "banshee: no session configs"
        return 0
    fi

    if ! command -v jq &>/dev/null; then
        echo "banshee: jq required to list sessions" >&2
        return 1
    fi

    local last=""
    [[ -f "$BANSHEE_LAST_FILE" ]] && last=$(head -n1 "$BANSHEE_LAST_FILE" 2>/dev/null | tr -d '\r\n')

    local f bundle_name sessions sn marker state sname
    for f in "${files[@]}"; do
        bundle_name=$(basename "$f" .json)
        marker=""
        [[ "$bundle_name" == "$last" ]] && marker=" (last)"
        sessions=$(jq -r '.sessions[].name' "$f" 2>/dev/null || true)
        printf "  %s%s\n" "$bundle_name" "$marker"
        if [[ -z "$sessions" ]]; then
            printf "    (invalid config)\n"
            continue
        fi
        while IFS= read -r sn; do
            [[ -z "$sn" ]] && continue
            sname=$(banshee_session_name "$sn")
            state="stopped"
            if banshee_has_tmux && tmux has-session -t "=$sname" 2>/dev/null; then
                state="running"
            fi
            printf "    %-24s [%s]\n" "$sn" "$state"
        done <<< "$sessions"
    done
}

# --- Startup prompt: offer to restore last loaded session if not all running ---
banshee_startup_prompt() {
    banshee_has_tmux || return 0
    [[ "${BANSHEE_STARTUP_PROMPT:-true}" == "true" ]] || return 0
    [[ -n "${TMUX:-}" ]] && return 0
    [[ -t 0 && -t 1 ]] || return 0
    [[ -n "${BANSHEE_STARTUP_CHECKED:-}" ]] && return 0
    export BANSHEE_STARTUP_CHECKED=1

    [[ -f "$BANSHEE_LAST_FILE" ]] || return 0
    command -v jq &>/dev/null || return 0

    local name
    name=$(head -n1 "$BANSHEE_LAST_FILE" 2>/dev/null | tr -d '\r\n')
    [[ -z "$name" ]] && return 0

    local cfg
    cfg=$(banshee_session_config_path "$name")
    [[ -f "$cfg" ]] || return 0

    local names_to_check
    names_to_check=$(jq -r '.sessions[].name' "$cfg" 2>/dev/null) || return 0
    [[ -z "$names_to_check" ]] && return 0

    local all_running=true sn sname
    while IFS= read -r sn; do
        [[ -z "$sn" ]] && continue
        sname=$(banshee_session_name "$sn")
        if ! tmux has-session -t "=$sname" 2>/dev/null; then
            all_running=false
            break
        fi
    done <<< "$names_to_check"

    $all_running && return 0

    printf "banshee: restore last session '%s'? [Y/n] " "$name"
    local reply=""
    read -r reply || return 0
    case "$reply" in
        ""|y|Y|yes|YES|Yes)
            banshee_restore_last
            ;;
    esac
}

# --- Usage ---
banshee_usage() {
    cat <<'EOF'
banshee - fluid git repository navigation powered by fzf

Usage:
  banshee [query]         Find and navigate to a git repository
  banshee -s <name>       Load session config <name> (open editor if new)
  banshee -se <name>      Edit existing session config <name>
  banshee -r              Restore last loaded session
  banshee -l              List session configs and their state
  banshee -c              Clear the repository cache
  banshee -v              Show version
  banshee -h              Show this help

Session configs: ~/.config/banshee/sessions/<name>.json (JSON, requires jq)
Configuration:   ~/.config/banshee/banshee.conf

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
            banshee_restore_last
            return $?
            ;;
        -s|--session)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -s requires a session name" >&2
                return 1
            fi
            banshee_edit_session "$2" load
            return $?
            ;;
        -se|--edit)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -se requires a session name" >&2
                return 1
            fi
            banshee_edit_session "$2" edit_only
            return $?
            ;;
        -l|--list)
            banshee_list_sessions
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
            local selected
            selected=$(banshee_select_repo "${1:-}") || return 1

            if banshee_has_tmux; then
                banshee_goto_repo "$selected"
            else
                echo "$selected"
            fi
            ;;
    esac
}

# Only run main if executed directly (not sourced)
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    banshee_main "$@"
fi
