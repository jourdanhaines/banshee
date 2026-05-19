#!/usr/bin/env bash
# banshee - fluid git repository navigation powered by fzf
# https://github.com/jourdanhaines/banshee

# Only apply strict mode when executed directly, not when sourced
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    set -euo pipefail
fi

BANSHEE_VERSION="0.3.0"
BANSHEE_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/banshee"
BANSHEE_DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/banshee"
BANSHEE_CONFIG_FILE="$BANSHEE_CONFIG_DIR/banshee.conf"
BANSHEE_SESSIONS_DIR="$BANSHEE_CONFIG_DIR/sessions"
BANSHEE_GROUPS_DIR="$BANSHEE_CONFIG_DIR/groups"
BANSHEE_CACHE_FILE="$BANSHEE_DATA_DIR/repo_cache"
BANSHEE_LAST_FILE="$BANSHEE_DATA_DIR/last_action"

# --- Defaults (overridable via config) ---
BANSHEE_SEARCH_PATHS=("$HOME")
BANSHEE_MAX_DEPTH=5
BANSHEE_KEYBIND="ctrl-f"
BANSHEE_FZF_OPTS=""
BANSHEE_CACHE_TTL=300
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

# --- Ensure directories exist + migrate stale state ---
banshee_init() {
    [[ -d "$BANSHEE_CONFIG_DIR" ]]   || command mkdir -p "$BANSHEE_CONFIG_DIR"
    [[ -d "$BANSHEE_SESSIONS_DIR" ]] || command mkdir -p "$BANSHEE_SESSIONS_DIR"
    [[ -d "$BANSHEE_GROUPS_DIR" ]]   || command mkdir -p "$BANSHEE_GROUPS_DIR"
    [[ -d "$BANSHEE_DATA_DIR" ]]     || command mkdir -p "$BANSHEE_DATA_DIR"

    # Migrate from very-old flat session/state files
    [[ -f "$BANSHEE_DATA_DIR/sessions" ]] && rm -f "$BANSHEE_DATA_DIR/sessions" 2>/dev/null
    [[ -f "$BANSHEE_DATA_DIR/session_state" ]] && rm -f "$BANSHEE_DATA_DIR/session_state" 2>/dev/null

    # Migrate 0.2.0 last_loaded → 0.3.0 last_action (prefix as target since it pointed at a bundle)
    if [[ -f "$BANSHEE_DATA_DIR/last_loaded" && ! -f "$BANSHEE_LAST_FILE" ]]; then
        local old
        old=$(head -n1 "$BANSHEE_DATA_DIR/last_loaded" 2>/dev/null | tr -d '\r\n')
        [[ -n "$old" ]] && printf 'target:%s\n' "$old" > "$BANSHEE_LAST_FILE"
        rm -f "$BANSHEE_DATA_DIR/last_loaded" 2>/dev/null
    fi

    banshee_load_config
}

# --- Find git repositories ---
banshee_find_repos() {
    local use_cache=false

    if [[ -f "$BANSHEE_CACHE_FILE" ]]; then
        local cache_age file_mtime
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

# --- Exact-match a target name against repo basenames. Echo single repo path or empty. ---
banshee_find_repo_exact() {
    local target="$1"
    [[ -z "$target" ]] && return 1
    local repos
    repos=$(banshee_find_repos)
    [[ -z "$repos" ]] && return 1
    local matches
    matches=$(echo "$repos" | while IFS= read -r r; do
        [[ "$(basename "$r")" == "$target" ]] && echo "$r"
    done)
    [[ -z "$matches" ]] && return 1
    local count
    count=$(echo "$matches" | wc -l)
    (( count == 1 )) || return 1
    echo "$matches"
}

# --- Select a repo via fzf (returns path) ---
banshee_select_repo() {
    local query="${1:-}"
    local repos
    repos=$(banshee_find_repos)

    [[ -z "$repos" ]] && echo "banshee: no git repositories found" >&2 && return 1

    if [[ -n "$query" ]]; then
        local exact
        if exact=$(banshee_find_repo_exact "$query"); then
            echo "$exact"
            return 0
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
        rpath=$(echo "$BANSHEE_REPO_LIST" | grep "|${name}$" | head -1 | cut -d"|" -f1)
        printf "\033[1;34m%s\033[0m\n" "$rpath"
        echo ""
        readme="$rpath/README.md"
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
    [[ "$raw" == */* ]] && raw=$(basename "$raw")
    echo "${raw//[.:]/_}"
}

# Create a plain (no-config) tmux session detached. Idempotent.
banshee_create_plain_session() {
    local name="$1" cwd="$2"
    banshee_has_tmux || return 1
    tmux has-session -t "=$name" 2>/dev/null && return 0
    tmux new-session -d -s "$name" -c "$cwd"
}

# Attach (outside tmux) or switch-client (inside tmux) to a session.
banshee_attach_or_switch() {
    local name="$1"
    banshee_has_tmux || return 1
    if [[ -n "${TMUX:-}" ]]; then
        tmux switch-client -t "=$name" 2>/dev/null || true
    else
        tmux attach-session -t "=$name"
    fi
}

# --- Repo cache ---
banshee_clear_cache() {
    rm -f "$BANSHEE_CACHE_FILE"
    echo "banshee: cache cleared"
}

# =============================================================================
# JSON / session-config subsystem
# =============================================================================

banshee_require_jq() {
    if ! command -v jq &>/dev/null; then
        echo "banshee: jq is required for session/group management" >&2
        echo "  install via your package manager (e.g. 'pacman -S jq', 'brew install jq', 'apt install jq')" >&2
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
                ed="$cand"; break
            fi
        done
    fi
    echo "$ed"
}

banshee_target_config_path() {
    echo "$BANSHEE_SESSIONS_DIR/${1}.json"
}

banshee_group_config_path() {
    echo "$BANSHEE_GROUPS_DIR/${1}.json"
}

banshee_write_default_template() {
    local cfg="$1" target="$2"
    cat > "$cfg" <<EOF
{
  "v": 1,
  "name": "$target",
  "windows": [
    {
      "name": "<window_name>",
      "panes": [
        { "run": "<target_command>" }
      ]
    }
  ]
}
EOF
}

banshee_write_default_group_template() {
    local cfg="$1" name="$2"
    local targets_json="$3"   # already a JSON array string like ["a","b"]
    cat > "$cfg" <<EOF
{
  "v": 1,
  "name": "$name",
  "targets": $targets_json
}
EOF
}

banshee_validate_config() {
    local cfg="$1"
    if ! jq empty "$cfg" 2>/dev/null; then
        echo "banshee: invalid JSON in $cfg" >&2
        return 1
    fi
    if jq -e 'has("sessions")' "$cfg" &>/dev/null; then
        echo "banshee: $cfg uses the 0.2.0 \"sessions\" wrapper which is no longer supported." >&2
        echo "  Split each session into its own ~/.config/banshee/sessions/<target>.json file (drop the wrapper)." >&2
        return 1
    fi
    if ! jq -e '.v == 1' "$cfg" &>/dev/null; then
        echo "banshee: $cfg missing or unsupported \"v\" (must be 1)" >&2
        return 1
    fi
    if ! jq -e '.name | type == "string" and length > 0' "$cfg" &>/dev/null; then
        echo "banshee: $cfg missing non-empty \"name\"" >&2
        return 1
    fi
    if ! jq -e '.windows | type == "array" and length > 0' "$cfg" &>/dev/null; then
        echo "banshee: $cfg \"windows\" must be a non-empty array" >&2
        return 1
    fi
    if ! jq -e '.windows | all(.panes | type == "array" and length > 0)' "$cfg" &>/dev/null; then
        echo "banshee: $cfg each window needs a non-empty \"panes\" array" >&2
        return 1
    fi
    return 0
}

banshee_validate_group_config() {
    local cfg="$1"
    if ! jq empty "$cfg" 2>/dev/null; then
        echo "banshee: invalid JSON in $cfg" >&2
        return 1
    fi
    if ! jq -e '.v == 1' "$cfg" &>/dev/null; then
        echo "banshee: $cfg missing or unsupported \"v\" (must be 1)" >&2
        return 1
    fi
    if ! jq -e '.name | type == "string" and length > 0' "$cfg" &>/dev/null; then
        echo "banshee: $cfg missing non-empty \"name\"" >&2
        return 1
    fi
    if ! jq -e '.targets | type == "array" and length > 0 and all(type == "string" and length > 0)' "$cfg" &>/dev/null; then
        echo "banshee: $cfg \"targets\" must be a non-empty array of strings" >&2
        return 1
    fi
    return 0
}

# Open editor on a target config; loop until valid JSON or user cancels.
# Args: <target> <mode>   mode = "load" | "no_load"
banshee_edit_session_config() {
    local target="$1" mode="$2"
    banshee_require_jq || return 1

    local cfg
    cfg=$(banshee_target_config_path "$target")
    [[ -d "$BANSHEE_SESSIONS_DIR" ]] || command mkdir -p "$BANSHEE_SESSIONS_DIR"

    local created=0
    if [[ ! -f "$cfg" ]]; then
        banshee_write_default_template "$cfg" "$target"
        created=1
    fi

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
        read -r reply || { (( created )) && rm -f "$cfg"; return 1; }
        case "$reply" in
            ""|r|R) continue ;;
            *) (( created )) && rm -f "$cfg"; return 1 ;;
        esac
    done

    if [[ "$mode" == "load" ]]; then
        banshee_resolve_and_load "$target" default true
        return $?
    fi
    return 0
}

# =============================================================================
# Pane layout — recursive walker (UNCHANGED from 0.2.0 logic)
# =============================================================================
# Args: <target_pane_id> <panes_json> <base_cwd> <depth>
# depth 0 → vertical splits (rows); depth 1 → horizontal (cols); alternating.
banshee_build_panes() {
    local target_pane="$1" panes_json="$2" base_cwd="$3" depth="$4"

    local len
    len=$(printf '%s' "$panes_json" | jq 'length')
    (( len == 0 )) && return 0

    local dir
    if (( depth % 2 == 0 )); then dir="-v"; else dir="-h"; fi

    local pane_ids="$target_pane"
    local prev_pane="$target_pane"

    local i percentage new_pane
    for ((i=1; i<len; i++)); do
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

# Build one tmux session from a per-target config JSON.
# Args: <target_name> <config_json> <default_cwd>
# target_name is authoritative for the tmux session name (overrides .name).
banshee_build_tmux_session() {
    local target_name="$1" cfg_json="$2" default_cwd="$3"

    local sname
    sname=$(banshee_session_name "$target_name")

    if tmux has-session -t "=$sname" 2>/dev/null; then
        echo "banshee: session '$sname' already running — skipping" >&2
        return 0
    fi

    local scwd
    scwd=$(printf '%s' "$cfg_json" | jq -r '.cwd // ""')
    [[ -z "$scwd" ]] && scwd="$default_cwd"
    scwd=$(banshee_expand_path "$scwd")
    [[ -d "$scwd" ]] || scwd="$HOME"

    local win_count
    win_count=$(printf '%s' "$cfg_json" | jq '.windows | length')

    local w wjson wname wcwd panes_json first_pane
    for ((w=0; w<win_count; w++)); do
        wjson=$(printf '%s' "$cfg_json" | jq -c ".windows[$w]")
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
    echo "banshee: built session '$sname'" >&2
}

# =============================================================================
# Resolve & load — the central target-loading dispatcher
# =============================================================================
# Args: <target> <mode> <attach>
#   mode:   "default"     load config if exists; else plain at repo; else error
#           "require_cfg" if config missing, drop into editor flow then load
#   attach: "true" / "false"
banshee_resolve_and_load() {
    local target="$1" mode="${2:-default}" attach="${3:-true}"
    [[ -z "$target" ]] && { echo "banshee: empty target" >&2; return 1; }
    banshee_has_tmux || { echo "banshee: tmux is not installed" >&2; return 1; }

    local cfg
    cfg=$(banshee_target_config_path "$target")
    local repo_path=""
    repo_path=$(banshee_find_repo_exact "$target" 2>/dev/null) || repo_path=""

    if [[ -f "$cfg" ]]; then
        banshee_require_jq || return 1
        banshee_validate_config "$cfg" || return 1
        local cfg_json default_cwd
        cfg_json=$(jq -c '.' "$cfg")
        default_cwd="${repo_path:-$HOME}"
        banshee_build_tmux_session "$target" "$cfg_json" "$default_cwd" || return 1
    else
        if [[ "$mode" == "require_cfg" ]]; then
            banshee_edit_session_config "$target" load
            return $?
        fi
        if [[ -z "$repo_path" ]]; then
            echo "banshee: no config or matching repo for '$target'" >&2
            return 1
        fi
        local sname
        sname=$(banshee_session_name "$target")
        banshee_create_plain_session "$sname" "$repo_path" || return 1
    fi

    banshee_record_last_action target "$target"

    if [[ "$attach" == "true" ]]; then
        local sname
        sname=$(banshee_session_name "$target")
        banshee_attach_or_switch "$sname"
    fi
}

# =============================================================================
# Groups
# =============================================================================

# Build the union pool of selectable target names: repos ∪ existing session configs.
banshee_target_pool() {
    {
        banshee_find_repos 2>/dev/null | while IFS= read -r p; do
            [[ -n "$p" ]] && basename "$p"
        done
        if [[ -d "$BANSHEE_SESSIONS_DIR" ]]; then
            shopt -s nullglob 2>/dev/null || true
            local f
            for f in "$BANSHEE_SESSIONS_DIR"/*.json; do
                [[ -e "$f" ]] || continue
                basename "$f" .json
            done
            shopt -u nullglob 2>/dev/null || true
        fi
    } | sort -u
}

# fzf multi-select. Args: <group_name> <current_csv>
# Outputs newline-separated selected targets in pick order; non-zero on cancel.
banshee_group_select_prompt() {
    local name="$1" current_csv="${2:-}"

    command -v fzf &>/dev/null || { echo "banshee: fzf required for group select" >&2; return 1; }

    local pool
    pool=$(banshee_target_pool)
    [[ -z "$pool" ]] && { echo "banshee: no targets available (no repos found, no session configs)" >&2; return 1; }

    # Reorder so current selections (if any) appear first in the list.
    local ordered="$pool"
    if [[ -n "$current_csv" ]]; then
        local first="" rest="" item
        local _rest="$current_csv" _item
        while [[ -n "$_rest" ]]; do
            _item="${_rest%%,*}"
            _item="${_item## }"; _item="${_item%% }"
            if [[ -n "$_item" ]] && echo "$pool" | command grep -qx "$_item"; then
                first+="$_item"$'\n'
            fi
            [[ "$_rest" == *,* ]] && _rest="${_rest#*,}" || _rest=""
        done
        # everything in pool not already in first
        rest=$(echo "$pool" | while IFS= read -r item; do
            [[ -z "$item" ]] && continue
            if ! echo "$first" | command grep -qx "$item"; then
                echo "$item"
            fi
        done)
        ordered="${first}${rest}"
    fi

    local header
    if [[ -n "$current_csv" ]]; then
        header="editing group '$name' — current: $current_csv — TAB to toggle, ENTER to confirm"
    else
        header="select targets for group '$name' — TAB to toggle, ENTER to confirm"
    fi

    local out
    out=$(printf '%s' "$ordered" | sed '/^$/d' | fzf --multi --layout=reverse --border --prompt="targets> " --header="$header") || return 1
    [[ -z "$out" ]] && return 1
    printf '%s\n' "$out"
}

banshee_write_group() {
    local name="$1" selections="$2"
    local targets_json
    targets_json=$(printf '%s\n' "$selections" | sed '/^$/d' | jq -R . | jq -s -c .)
    local cfg
    cfg=$(banshee_group_config_path "$name")
    [[ -d "$BANSHEE_GROUPS_DIR" ]] || command mkdir -p "$BANSHEE_GROUPS_DIR"
    banshee_write_default_group_template "$cfg" "$name" "$targets_json"
    banshee_validate_group_config "$cfg" || return 1
    return 0
}

banshee_load_group() {
    local name="$1"
    banshee_require_jq || return 1
    banshee_has_tmux || { echo "banshee: tmux is not installed" >&2; return 1; }

    local cfg
    cfg=$(banshee_group_config_path "$name")

    if [[ ! -f "$cfg" ]]; then
        local selections
        selections=$(banshee_group_select_prompt "$name" "") || { echo "banshee: group creation cancelled" >&2; return 1; }
        banshee_write_group "$name" "$selections" || return 1
    fi

    banshee_validate_group_config "$cfg" || return 1

    local targets first=""
    targets=$(jq -r '.targets[]' "$cfg")
    [[ -z "$targets" ]] && { echo "banshee: group '$name' has no targets" >&2; return 1; }

    local t
    while IFS= read -r t; do
        [[ -z "$t" ]] && continue
        [[ -z "$first" ]] && first="$t"
        banshee_resolve_and_load "$t" default false || echo "banshee: target '$t' failed to load" >&2
    done <<< "$targets"

    banshee_record_last_action group "$name"

    [[ -z "$first" ]] && return 0
    local fname
    fname=$(banshee_session_name "$first")
    banshee_attach_or_switch "$fname"
}

banshee_edit_group() {
    local name="$1"
    banshee_require_jq || return 1

    local cfg
    cfg=$(banshee_group_config_path "$name")

    local current_csv=""
    if [[ -f "$cfg" ]] && banshee_validate_group_config "$cfg" 2>/dev/null; then
        current_csv=$(jq -r '.targets | join(",")' "$cfg")
    fi

    local selections
    selections=$(banshee_group_select_prompt "$name" "$current_csv") || { echo "banshee: edit cancelled" >&2; return 1; }
    banshee_write_group "$name" "$selections" || return 1
    echo "banshee: group '$name' saved"
}

# =============================================================================
# Last action tracking
# =============================================================================

# Args: <type: target|group> <name>
banshee_record_last_action() {
    local type="$1" name="$2"
    [[ -z "$type" || -z "$name" ]] && return 0
    local tmp="${BANSHEE_LAST_FILE}.tmp.$$"
    printf '%s:%s\n' "$type" "$name" > "$tmp"
    mv -f "$tmp" "$BANSHEE_LAST_FILE"
}

banshee_read_last_action() {
    [[ -f "$BANSHEE_LAST_FILE" ]] || return 1
    head -n1 "$BANSHEE_LAST_FILE" 2>/dev/null | tr -d '\r\n'
}

banshee_restore_last_action() {
    local entry
    entry=$(banshee_read_last_action) || { echo "banshee: no previous action" >&2; return 1; }
    [[ -z "$entry" ]] && { echo "banshee: no previous action" >&2; return 1; }
    local type="${entry%%:*}"
    local name="${entry#*:}"
    [[ -z "$type" || -z "$name" || "$type" == "$name" ]] && { echo "banshee: malformed last_action: $entry" >&2; return 1; }
    case "$type" in
        target) banshee_resolve_and_load "$name" default true ;;
        group)  banshee_load_group "$name" ;;
        *) echo "banshee: unknown last_action type '$type'" >&2; return 1 ;;
    esac
}

# =============================================================================
# List
# =============================================================================

banshee_list_all() {
    local last_entry="" last_type="" last_name=""
    if last_entry=$(banshee_read_last_action 2>/dev/null); then
        last_type="${last_entry%%:*}"
        last_name="${last_entry#*:}"
    fi

    local printed_target=0 printed_group=0

    # Targets
    shopt -s nullglob 2>/dev/null || true
    local target_files=("$BANSHEE_SESSIONS_DIR"/*.json)
    shopt -u nullglob 2>/dev/null || true
    if (( ${#target_files[@]} > 0 )); then
        echo "Targets:"
        printed_target=1
        local f tname sname state marker
        for f in "${target_files[@]}"; do
            tname=$(basename "$f" .json)
            sname=$(banshee_session_name "$tname")
            state="stopped"
            if banshee_has_tmux && tmux has-session -t "=$sname" 2>/dev/null; then
                state="running"
            fi
            marker=""
            [[ "$last_type" == "target" && "$last_name" == "$tname" ]] && marker=" (last)"
            printf "  %-24s [%s]%s\n" "$tname" "$state" "$marker"
        done
    fi

    # Groups
    shopt -s nullglob 2>/dev/null || true
    local group_files=("$BANSHEE_GROUPS_DIR"/*.json)
    shopt -u nullglob 2>/dev/null || true
    if (( ${#group_files[@]} > 0 )); then
        (( printed_target )) && echo ""
        echo "Groups:"
        printed_group=1
        if ! command -v jq &>/dev/null; then
            echo "  (jq required to list group contents)"
            return 0
        fi
        local f gname targets marker t tsname tstate
        for f in "${group_files[@]}"; do
            gname=$(basename "$f" .json)
            marker=""
            [[ "$last_type" == "group" && "$last_name" == "$gname" ]] && marker=" (last)"
            printf "  %s%s\n" "$gname" "$marker"
            if ! banshee_validate_group_config "$f" 2>/dev/null; then
                printf "    (invalid group config)\n"
                continue
            fi
            targets=$(jq -r '.targets[]' "$f")
            while IFS= read -r t; do
                [[ -z "$t" ]] && continue
                tsname=$(banshee_session_name "$t")
                tstate="stopped"
                if banshee_has_tmux && tmux has-session -t "=$tsname" 2>/dev/null; then
                    tstate="running"
                fi
                printf "    %-22s [%s]\n" "$t" "$tstate"
            done <<< "$targets"
        done
    fi

    if (( ! printed_target && ! printed_group )); then
        echo "banshee: no session configs or groups"
    fi
}

# =============================================================================
# Startup prompt
# =============================================================================
banshee_startup_prompt() {
    banshee_has_tmux || return 0
    [[ "${BANSHEE_STARTUP_PROMPT:-true}" == "true" ]] || return 0
    [[ -n "${TMUX:-}" ]] && return 0
    [[ -t 0 && -t 1 ]] || return 0
    [[ -n "${BANSHEE_STARTUP_CHECKED:-}" ]] && return 0
    export BANSHEE_STARTUP_CHECKED=1

    local entry
    entry=$(banshee_read_last_action) || return 0
    [[ -z "$entry" ]] && return 0
    local type="${entry%%:*}" name="${entry#*:}"
    [[ -z "$type" || -z "$name" ]] && return 0

    command -v jq &>/dev/null || return 0

    local all_running=true
    case "$type" in
        target)
            local sname
            sname=$(banshee_session_name "$name")
            tmux has-session -t "=$sname" 2>/dev/null || all_running=false
            ;;
        group)
            local cfg
            cfg=$(banshee_group_config_path "$name")
            [[ -f "$cfg" ]] || return 0
            local targets t tsname
            targets=$(jq -r '.targets[]' "$cfg" 2>/dev/null) || return 0
            [[ -z "$targets" ]] && return 0
            while IFS= read -r t; do
                [[ -z "$t" ]] && continue
                tsname=$(banshee_session_name "$t")
                tmux has-session -t "=$tsname" 2>/dev/null || { all_running=false; break; }
            done <<< "$targets"
            ;;
        *) return 0 ;;
    esac

    $all_running && return 0

    printf "banshee: restore last %s '%s'? [Y/n] " "$type" "$name"
    local reply=""
    read -r reply || return 0
    case "$reply" in
        ""|y|Y|yes|YES|Yes) banshee_restore_last_action ;;
    esac
}

# =============================================================================
# Usage + main dispatch
# =============================================================================
banshee_usage() {
    cat <<'EOF'
banshee - fluid git repo navigation + declarative tmux sessions

Usage:
  banshee [query]         fzf repo picker → load target (config if defined, else plain)
  banshee <target>        Load target directly (exact-match)
  banshee -s <target>     Load target; if no config, open $EDITOR to create one
  banshee -se <target>    Edit (or create) target session config; no load
  banshee -g <name>       Load group; if missing, prompt multi-select to create
  banshee -ge <name>      Edit (or create) group via multi-select; no load
  banshee -r              Re-run last action (target or group)
  banshee -l              List session configs and groups
  banshee -c              Clear repository cache
  banshee -v              Show version
  banshee -h              Show this help

Configs: ~/.config/banshee/sessions/<target>.json  (per-target)
Groups:  ~/.config/banshee/groups/<name>.json
Config:  ~/.config/banshee/banshee.conf
Requires: fzf, git, jq (and tmux for session features)

EOF
}

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
            banshee_restore_last_action
            return $?
            ;;
        -s|--session)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -s requires a target name" >&2
                return 1
            fi
            banshee_resolve_and_load "$2" require_cfg true
            return $?
            ;;
        -se|--edit-session)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -se requires a target name" >&2
                return 1
            fi
            banshee_edit_session_config "$2" no_load
            return $?
            ;;
        -g|--group)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -g requires a group name" >&2
                return 1
            fi
            banshee_load_group "$2"
            return $?
            ;;
        -ge|--edit-group)
            if [[ -z "${2:-}" ]]; then
                echo "banshee: -ge requires a group name" >&2
                return 1
            fi
            banshee_edit_group "$2"
            return $?
            ;;
        -l|--list)
            banshee_list_all
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
            local query="${1:-}"
            local target=""
            if [[ -n "$query" ]]; then
                # Exact-match against existing target config or repo first
                if [[ -f "$(banshee_target_config_path "$query")" ]]; then
                    target="$query"
                elif banshee_find_repo_exact "$query" >/dev/null 2>&1; then
                    target="$query"
                fi
            fi
            if [[ -z "$target" ]]; then
                local selected
                selected=$(banshee_select_repo "$query") || return 1
                target=$(basename "$selected")
            fi
            banshee_resolve_and_load "$target" default true
            return $?
            ;;
    esac
}

# Only run main if executed directly (not sourced)
if [[ -n "${BASH_SOURCE+x}" && "${BASH_SOURCE[0]}" == "${0}" ]] \
    || [[ -n "${ZSH_EVAL_CONTEXT+x}" && "$ZSH_EVAL_CONTEXT" == "toplevel" ]]; then
    banshee_main "$@"
fi
