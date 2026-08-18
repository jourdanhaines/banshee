#!/bin/sh
# banshee claude-code plugin — desktop notifications when Claude Code needs
# your input (plan approval, permission prompts, questions) or finishes.
#
# A background plugin (manifest "background": true): banshee starts it with
# the daemon and keeps it running. It listens on a FIFO for events written by
# hook.sh (which you register in ~/.claude/settings.json — see that file's
# header) and translates them into "notify" protocol messages. Clicking a
# notification focuses the Hyprland window running that Claude session.
#
# Options live in ./config (sourced as sh). Protocol reference:
# internal/providers/plugins/proto.go.
set -u

DIR="${BANSHEE_PLUGIN_DIR:-$(dirname "$0")}"
# shellcheck disable=SC1091
[ -f "$DIR/config" ] && . "$DIR/config"
REQUIRE_INPUT="${REQUIRE_INPUT:-true}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-10}"
EVENTS="${EVENTS:-Notification Stop}"
SOUND_FILE="${SOUND_FILE:-}"

RUNDIR="${XDG_RUNTIME_DIR:-/tmp}/banshee"
FIFO="$RUNDIR/claude-code.fifo"
# Session id -> hook ppid, appended per hook; the last line for an id wins.
STATE="$RUNDIR/claude-code.state"

mkdir -p "$RUNDIR"
rm -f "$FIFO"
mkfifo -m 600 "$FIFO"
: > "$STATE"

# --- tiny JSON helpers (same approach as plugins/example) --------------------

str_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

num_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\\([0-9][0-9]*\\).*/\\1/p"
}

json_escape() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# --- hook events from the FIFO ----------------------------------------------

# handle_hook <line> — one hook invocation forwarded by hook.sh. Emits one
# atomic notify line on stdout (single printf, well under PIPE_BUF, so it
# cannot interleave with the foreground loop's writes).
handle_hook() {
    line=$1
    event=$(str_field "$line" hook_event_name)
    [ -n "$event" ] || return 0
    case " $EVENTS " in
        *" $event "*) ;;
        *) return 0 ;;
    esac

    sid=$(str_field "$line" session_id)
    message=$(str_field "$line" message)
    cwd=$(str_field "$line" cwd)
    ppid=$(num_field "$line" ppid)
    id="claude:${sid:-default}"
    [ -n "$ppid" ] && printf '%s %s\n' "$id" "$ppid" >> "$STATE"

    case "$event" in
        Notification) summary="Claude Code needs input" ;;
        Stop)         summary="Claude Code finished" ;;
        *)            summary="Claude Code: $event" ;;
    esac
    body="${message:-$event}"
    [ -n "$cwd" ] && body="$body — $cwd"

    if [ "$REQUIRE_INPUT" = "true" ]; then
        opts='"require_input":true,"timeout_ms":0'
    else
        opts='"require_input":false,"timeout_ms":'$((TIMEOUT_SECONDS * 1000))
    fi
    [ -n "$SOUND_FILE" ] && opts=$opts',"sound":"'$(json_escape "$SOUND_FILE")'"'
    printf '{"v":1,"event":"notify","notify":{"id":"%s","summary":"%s","body":"%s","icon":"dialog-question-symbolic",%s,"actions":[{"key":"default","label":"Focus"}]}}\n' \
        "$(json_escape "$id")" "$(json_escape "$summary")" "$(json_escape "$body")" "$opts"
}

# The FIFO reader runs in the background; the outer loop reopens the FIFO
# after every writer generation closes it (read returns EOF then).
(
    while :; do
        while IFS= read -r line; do
            [ -n "$line" ] && handle_hook "$line"
        done < "$FIFO"
    done
) &
READER_PID=$!

# --- focus-on-click ---------------------------------------------------------

# focus_terminal <notification-id> — walk the recorded hook pid's /proc
# ancestry until a pid matches a Hyprland client, then focus that window.
# Degrades silently without hyprctl or jq, or when the process is gone.
focus_terminal() {
    pid=$(awk -v k="$1" '$1 == k { p = $2 } END { print p }' "$STATE" 2>/dev/null)
    [ -n "$pid" ] || return 0
    command -v hyprctl >/dev/null 2>&1 || return 0
    command -v jq >/dev/null 2>&1 || return 0
    clients=$(hyprctl clients -j 2>/dev/null) || return 0
    while [ -n "$pid" ] && [ "$pid" -gt 1 ] 2>/dev/null; do
        addr=$(printf '%s' "$clients" | jq -r --argjson p "$pid" \
            '[.[] | select(.pid == $p) | .address] | first // empty' 2>/dev/null)
        if [ -n "$addr" ]; then
            hyprctl dispatch focuswindow "address:$addr" >/dev/null 2>&1
            return 0
        fi
        pid=$(awk '/^PPid:/ { print $2 }' "/proc/$pid/status" 2>/dev/null)
    done
    return 0
}

# --- protocol loop ----------------------------------------------------------

while IFS= read -r line; do
    case "$line" in
        '') continue ;;
    esac
    event=$(str_field "$line" event)
    case "$event" in
        query)
            # A background plugin on an old banshee (which ignores the
            # "background" manifest key) is started lazily and queried like
            # any other; answering empty keeps that degradation harmless.
            printf '{"v":1,"seq":%s,"event":"results","results":[],"done":true}\n' \
                "$(num_field "$line" seq)"
            ;;
        notify-action)
            focus_terminal "$(str_field "$line" id)"
            ;;
        notify-closed)
            ;;
        shutdown)
            kill "$READER_PID" 2>/dev/null
            rm -f "$FIFO" "$STATE"
            exit 0
            ;;
        *)
            ;;
    esac
done

kill "$READER_PID" 2>/dev/null
rm -f "$FIFO" "$STATE"
