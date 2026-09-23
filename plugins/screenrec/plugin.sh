#!/bin/sh
# banshee screenrec plugin — screen recording from the launcher. Type "rec" (or
# any longer prefix of "record") to record a region, a window or the focused
# screen (MP4 or GIF, optional audio), stop it again, and replay or copy recent
# recordings. A finished recording opens in mpv and lands on the clipboard as a
# file:// URI.
#
# Requires: wf-recorder slurp mpv wl-clipboard (wl-copy) jq hyprctl ffmpeg.
# Optional: pactl or wpctl (audio source probe), setsid from util-linux (keeps
# a recording alive across `banshee reload`). To float the replay window, add
# to hyprland.conf:
#   windowrulev2 = float, class:^(mpv)$
#
# Options live in ./config (sourced as sh). Protocol reference:
# internal/providers/plugins/proto.go.
set -u

DIR="${BANSHEE_PLUGIN_DIR:-$(dirname "$0")}"
if [ -z "${XDG_VIDEOS_DIR:-}" ]; then
    USER_DIRS="${XDG_CONFIG_HOME:-$HOME/.config}/user-dirs.dirs"
    # shellcheck disable=SC1090
    [ -f "$USER_DIRS" ] && . "$USER_DIRS" 2>/dev/null
fi
# shellcheck disable=SC1091
[ -f "$DIR/config" ] && . "$DIR/config"
OUTPUT_DIR="${OUTPUT_DIR:-${XDG_VIDEOS_DIR:-$HOME/Videos}/Recordings}"
FILENAME_PREFIX="${FILENAME_PREFIX:-screenrec}"
DEFAULT_FORMAT="${DEFAULT_FORMAT:-mp4}"
CODEC="${CODEC:-}"
WF_RECORDER_ARGS="${WF_RECORDER_ARGS:-}"
GIF_FPS="${GIF_FPS:-15}"
GIF_WIDTH="${GIF_WIDTH:-}"
MPV_ARGS="${MPV_ARGS:---loop=inf --really-quiet}"
AUTOPLAY="${AUTOPLAY:-true}"
COPY_TO_CLIPBOARD="${COPY_TO_CLIPBOARD:-true}"
RECENT_LIMIT="${RECENT_LIMIT:-10}"
NOTIFY_TIMEOUT_SECONDS="${NOTIFY_TIMEOUT_SECONDS:-8}"

PREFIX="$FILENAME_PREFIX"
RUNDIR="${XDG_RUNTIME_DIR:-/tmp}/banshee"
STATE="$RUNDIR/screenrec.state"
WFLOG="$RUNDIR/screenrec.log"
# Hidden, so recent/clear never glob it; same filesystem, so the final mv is atomic.
PARTIAL="$OUTPUT_DIR/.partial"
NOTIFY_ID=screenrec:rec
AUDIO_AVAILABLE=""
PHASE=""

mkdir -p "$RUNDIR"

HAVE_SETSID=""
command -v setsid >/dev/null 2>&1 && HAVE_SETSID=1

# --- tiny JSON helpers (same approach as plugins/example) --------------------

str_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

num_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\\([0-9][0-9]*\\).*/\\1/p"
}

# json_escape <text> — also flattens control characters (tool logs carry
# newlines), which would otherwise break the one-line JSON message.
json_escape() {
    printf '%s' "$1" | tr '\n\r\t' '   ' | tr -d '\000-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

lower() {
    printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

OUTPUT_DIR_JSON=$(json_escape "$OUTPUT_DIR")

# --- recording state ----------------------------------------------------------

# state_get <key> — one value from the state file, which is parsed, never sourced.
state_get() {
    [ -f "$STATE" ] && sed -n "s/^$1=//p" "$STATE" | head -n 1
}

# state_write <key=value>... — replaces the whole state file atomically.
state_write() {
    : > "$STATE.tmp"
    for kv in "$@"; do
        printf '%s\n' "$kv" >> "$STATE.tmp"
    done
    mv -f "$STATE.tmp" "$STATE"
}

state_clear() {
    rm -f "$STATE" "$STATE.tmp"
}

# alive <pid> — true while the process runs; an unreaped zombie counts as gone.
alive() {
    [ -n "$1" ] && kill -0 "$1" 2>/dev/null || return 1
    case $(sed -n 's/.*) \(.\) .*/\1/p' "/proc/$1/stat" 2>/dev/null) in
        Z) return 1 ;;
    esac
    return 0
}

# refresh_phase — sets PHASE to recording/finishing, or empty. A dead recorder
# left a partial file without its index (unplayable), so it is discarded.
refresh_phase() {
    PHASE=""
    [ -f "$STATE" ] || return 0
    rp_phase=$(state_get phase)
    rp_pid=$(state_get pid)
    if alive "$rp_pid"; then
        PHASE=$rp_phase
        return 0
    fi
    if [ "$rp_phase" = recording ]; then
        rp_file=$(state_get file)
        if [ -n "$rp_file" ] && [ -f "$rp_file" ]; then
            rm -f "$rp_file"
            notify_info "Recording lost" "${rp_file##*/} was interrupted and discarded"
        fi
    fi
    state_clear
}

# --- notifications (one printf each, so background writers never interleave) --

notify_recording() {
    printf '{"v":1,"event":"notify","notify":{"id":"%s","summary":"Recording…","body":"%s · %s","icon":"camera-video-symbolic","require_input":true,"timeout_ms":0,"actions":[{"key":"stop","label":"Stop"}]}}\n' \
        "$NOTIFY_ID" "$(json_escape "$1")" "$(json_escape "${2##*/}")"
}

notify_converting() {
    printf '{"v":1,"event":"notify","notify":{"id":"%s","summary":"Converting to GIF…","body":"%s","icon":"camera-video-symbolic","require_input":true,"timeout_ms":0,"actions":[]}}\n' \
        "$NOTIFY_ID" "$(json_escape "${1##*/}")"
}

notify_saved() {
    ns_body=${1##*/}
    [ "$COPY_TO_CLIPBOARD" = true ] && ns_body="$ns_body · copied to clipboard"
    printf '{"v":1,"event":"notify","notify":{"id":"%s","summary":"Recording saved","body":"%s","icon":"video-x-generic-symbolic","require_input":false,"timeout_ms":%s,"actions":[{"key":"default","label":"Open folder"}]}}\n' \
        "$NOTIFY_ID" "$(json_escape "$ns_body")" "$((NOTIFY_TIMEOUT_SECONDS * 1000))"
}

notify_failed() {
    printf '{"v":1,"event":"notify","notify":{"id":"%s","summary":"Recording failed","body":"%s","icon":"dialog-error-symbolic","require_input":false,"timeout_ms":%s,"actions":[]}}\n' \
        "$NOTIFY_ID" "$(json_escape "$1")" "$((NOTIFY_TIMEOUT_SECONDS * 1000))"
}

notify_info() {
    printf '{"v":1,"event":"notify","notify":{"id":"screenrec:info","summary":"%s","body":"%s","icon":"camera-video-symbolic","require_input":false,"timeout_ms":3000,"actions":[]}}\n' \
        "$(json_escape "$1")" "$(json_escape "$2")"
}

# --- dependencies and form options -------------------------------------------

# missing_deps — the required tools absent from PATH, space-separated.
missing_deps() {
    md=""
    for tool in wf-recorder slurp mpv wl-copy hyprctl jq ffmpeg; do
        command -v "$tool" >/dev/null 2>&1 || md="$md $tool"
    done
    printf '%s' "${md# }"
}

# dep_packages <tools> — Arch package names for the given tools.
dep_packages() {
    dp=""
    for tool in $1; do
        case $tool in
            wl-copy) dp="$dp wl-clipboard" ;;
            hyprctl) dp="$dp hyprland" ;;
            *) dp="$dp $tool" ;;
        esac
    done
    printf '%s' "${dp# }"
}

emit_deps_row() {
    printf '{"v":1,"seq":%s,"event":"results","done":true,"results":[{"id":"deps","title":"screenrec: install %s","subtitle":"sudo pacman -S --needed %s","icon":"dialog-warning-symbolic","score":100,"action":{"kind":"callback"}}]}\n' \
        "$1" "$2" "$(dep_packages "$2")"
}

# audio_options_json — probed once per process: the query path runs under
# the manifest's soft timeout. No probe tool at all assumes a source exists.
audio_options_json() {
    if [ -z "$AUDIO_AVAILABLE" ]; then
        if command -v pactl >/dev/null 2>&1; then
            if pactl list short sources </dev/null 2>/dev/null | grep -q .; then
                AUDIO_AVAILABLE=yes
            else
                AUDIO_AVAILABLE=no
            fi
        elif command -v wpctl >/dev/null 2>&1; then
            if wpctl status </dev/null 2>/dev/null | grep -q 'Sources:'; then
                AUDIO_AVAILABLE=yes
            else
                AUDIO_AVAILABLE=no
            fi
        else
            AUDIO_AVAILABLE=yes
        fi
    fi
    if [ "$AUDIO_AVAILABLE" = yes ]; then
        printf '%s' '["Off","On"]'
    else
        printf '%s' '["Unavailable (no audio source)"]'
    fi
}

format_options_json() {
    if [ "$(lower "$DEFAULT_FORMAT")" = gif ]; then
        printf '%s' '["GIF","MP4"]'
    else
        printf '%s' '["MP4","GIF"]'
    fi
}

# --- query ---------------------------------------------------------------------

# list_recordings — finished recordings, newest first, one path per line.
# Names with a quote or backslash are skipped rather than JSON-escaped.
list_recordings() {
    ls -td -- "$OUTPUT_DIR/$PREFIX"-*.mp4 "$OUTPUT_DIR/$PREFIX"-*.gif 2>/dev/null |
        while IFS= read -r lr; do
            case $lr in
                *'"'* | *'\'*) continue ;;
            esac
            [ -f "$lr" ] && printf '%s\n' "$lr"
        done
}

# stat_lines — "<size> <mtime> <path>" for each path on stdin, in one stat call.
stat_lines() {
    set --
    while IFS= read -r sl; do
        set -- "$@" "$sl"
    done
    [ $# -gt 0 ] && stat -c '%s %Y %n' -- "$@" 2>/dev/null
}

# matches_filter <title> — case-insensitive substring match against FILTER.
matches_filter() {
    [ -z "$FILTER" ] && return 0
    case $(lower "$1") in
        *"$FILTER"*) return 0 ;;
    esac
    return 1
}

# record_row <id> <title> <subtitle> <icon> <score> — a row opening the
# format/audio form; submission comes back as a submit event.
record_row() {
    matches_filter "$2" || return 0
    ROWS="$ROWS,{\"id\":\"$1\",\"title\":\"$2\",\"subtitle\":\"$3\",\"icon\":\"$4\",\"score\":$5,\"form\":{\"title\":\"$2\",\"fields\":[{\"key\":\"format\",\"label\":\"Format\",\"options\":$FORMAT_OPTS},{\"key\":\"audio\",\"label\":\"Audio\",\"options\":$AUDIO_OPTS}],\"submit_label\":\"Start Recording\"}}"
}

# emit_idle <seq> — record rows, then the clear row and recent recordings.
emit_idle() {
    ROWS=""
    FORMAT_OPTS=$(format_options_json)
    # Probe in this shell first: a $(...) subshell would not keep the cache.
    audio_options_json >/dev/null
    AUDIO_OPTS=$(audio_options_json)
    record_row rec:region "Record region" "Drag a region with slurp" camera-video-symbolic 100
    record_row rec:window "Record window" "Pick a window" window-new-symbolic 95
    record_row rec:screen "Record screen" "Focused monitor" video-display-symbolic 90
    ROWS="$ROWS$(list_recordings | stat_lines | awk \
        -v now="$(date +%s)" -v lim="$RECENT_LIMIT" -v f="$FILTER" -v dir="$OUTPUT_DIR_JSON" '
        function human(b) {
            if (b < 1024) return b " B"
            if (b < 1048576) return sprintf("%.1f KB", b / 1024)
            if (b < 1073741824) return sprintf("%.1f MB", b / 1048576)
            return sprintf("%.1f GB", b / 1073741824)
        }
        function age(s) {
            if (s < 0) s = 0
            if (s < 60) return s "s ago"
            if (s < 3600) return int(s / 60) "m ago"
            if (s < 86400) return int(s / 3600) "h ago"
            return int(s / 86400) "d ago"
        }
        {
            size = $1; mt = $2; name = $0
            sub(/^[^ ]* [^ ]* /, "", name); sub(/.*\//, "", name)
            n++; total += size
            if (n <= lim && (f == "" || index(tolower(name), f)))
                rec = rec sprintf(",{\"id\":\"recent:%s\",\"title\":\"%s\",\"subtitle\":\"%s · %s\",\"icon\":\"video-x-generic-symbolic\",\"score\":%d,\"action\":{\"kind\":\"callback\"}}", name, name, human(size), age(now - mt), 70 - (n - 1))
        }
        END {
            if (n > 0) {
                t = sprintf("Clear recordings (%d files, %.1f MB)", n, total / 1048576)
                if (f == "" || index(tolower(t), f))
                    printf ",{\"id\":\"clear\",\"title\":\"%s\",\"icon\":\"user-trash-symbolic\",\"score\":80,\"form\":{\"title\":\"Delete %d recordings from %s?\",\"fields\":[{\"key\":\"confirm\",\"label\":\"Type delete to confirm\",\"placeholder\":\"delete\",\"required\":true}],\"submit_label\":\"Delete\"}}", t, n, dir
            }
            printf "%s", rec
        }')"
    printf '%s\n' "{\"v\":1,\"seq\":$1,\"event\":\"results\",\"done\":true,\"results\":[${ROWS#,}]}"
}

# emit_recording <seq> — the single Stop row while wf-recorder runs.
emit_recording() {
    er_started=$(date -d "@$(state_get started)" +%H:%M:%S 2>/dev/null)
    er_final=$(state_get final)
    er_sub=$(json_escape "$(state_get target) · started $er_started · ${er_final##*/}")
    printf '%s\n' "{\"v\":1,\"seq\":$1,\"event\":\"results\",\"done\":true,\"results\":[{\"id\":\"stop\",\"title\":\"Stop recording\",\"subtitle\":\"$er_sub\",\"icon\":\"media-playback-stop-symbolic\",\"score\":100,\"action\":{\"kind\":\"callback\"}}]}"
}

# emit_finishing <seq> — a placeholder row while the stop pipeline runs.
emit_finishing() {
    ef_title="Finishing recording…"
    [ "$(state_get format)" = gif ] && ef_title="Converting to GIF…"
    ef_final=$(state_get final)
    printf '%s\n' "{\"v\":1,\"seq\":$1,\"event\":\"results\",\"done\":true,\"results\":[{\"id\":\"finishing\",\"title\":\"$ef_title\",\"subtitle\":\"$(json_escape "${ef_final##*/}")\",\"icon\":\"camera-video-symbolic\",\"score\":100,\"action\":{\"kind\":\"callback\"}}]}"
}

handle_query() {
    hq_missing=$(missing_deps)
    if [ -n "$hq_missing" ]; then
        emit_deps_row "$1" "$hq_missing"
        return 0
    fi
    FILTER=$(lower "$2")
    refresh_phase
    case $PHASE in
        recording) emit_recording "$1" ;;
        finishing) emit_finishing "$1" ;;
        *) emit_idle "$1" ;;
    esac
}

# --- target pickers (print the target, or nothing when cancelled) ---------------

pick_region() {
    slurp </dev/null 2>/dev/null
}

# pick_window — slurp over the mapped, visible clients on each monitor's
# active and special workspace (the hyprshot approach).
pick_window() {
    pw_ws=$(hyprctl monitors -j </dev/null 2>/dev/null |
        jq -c '[.[] | .activeWorkspace.id, .specialWorkspace.id | select(. != null and . != 0)]' 2>/dev/null)
    [ -n "$pw_ws" ] || return 0
    pw_boxes=$(hyprctl clients -j </dev/null 2>/dev/null |
        jq -r --argjson ws "$pw_ws" '.[] | select(.mapped and (.hidden | not)) |
            select(.workspace.id as $w | any($ws[]; . == $w)) |
            "\(.at[0]),\(.at[1]) \(.size[0])x\(.size[1])"' 2>/dev/null)
    [ -n "$pw_boxes" ] || return 0
    printf '%s\n' "$pw_boxes" | slurp -r 2>/dev/null
}

focused_output() {
    hyprctl monitors -j </dev/null 2>/dev/null |
        jq -r '.[] | select(.focused) | .name' 2>/dev/null | head -n 1
}

# --- recording lifecycle ---------------------------------------------------------

# spawn <cmd>... — runs a tool in its own session when setsid exists, so the
# host's process-group kill on reload/shutdown does not take it down.
spawn() {
    if [ -n "$HAVE_SETSID" ]; then
        setsid "$@"
    else
        "$@"
    fi
}

# start_recording <mode> <format> <audio> — pick the target, then launch
# wf-recorder into $PARTIAL; format and audio are the form's option strings.
start_recording() {
    refresh_phase
    if [ -n "$PHASE" ]; then
        notify_info "Already recording" "Stop the current recording first"
        return 0
    fi
    sr_format=mp4
    [ "$(lower "$2")" = gif ] && sr_format=gif
    sr_audio=""
    [ "$sr_format" = mp4 ] && [ "$(lower "$3")" = on ] && sr_audio=1

    sr_geom=""
    sr_out=""
    case $1 in
        region) sr_geom=$(pick_region) ;;
        window) sr_geom=$(pick_window) ;;
        screen) sr_out=$(focused_output) ;;
        *) return 0 ;;
    esac
    if [ "$1" = screen ]; then
        [ -n "$sr_out" ] || return 0
        sr_target="screen $sr_out"
    else
        [ -n "$sr_geom" ] || return 0
        sr_target="$1 $sr_geom"
    fi

    mkdir -p "$PARTIAL"
    sr_name="$PREFIX-$(date +%Y%m%d-%H%M%S)"
    sr_file="$PARTIAL/$sr_name.mp4"
    sr_final="$OUTPUT_DIR/$sr_name.$sr_format"

    # -y: a stale partial with the same second-stamped name would otherwise
    # make wf-recorder prompt on stdin.
    set -- wf-recorder -y -f "$sr_file"
    if [ -n "$sr_geom" ]; then
        set -- "$@" -g "$sr_geom"
    else
        set -- "$@" -o "$sr_out"
    fi
    [ "$sr_format" = mp4 ] && [ -n "$CODEC" ] && set -- "$@" -c "$CODEC"
    [ -n "$sr_audio" ] && set -- "$@" --audio
    # shellcheck disable=SC2086 # WF_RECORDER_ARGS is word-split on purpose.
    set -- "$@" $WF_RECORDER_ARGS
    [ -n "$HAVE_SETSID" ] && set -- setsid "$@"

    : > "$WFLOG"
    # A non-interactive shell's & child is not a process-group leader, so
    # util-linux setsid execs in place: $! is wf-recorder's own pid. The child
    # starts with SIGINT ignored; wf-recorder installs its own handler anyway.
    "$@" </dev/null >/dev/null 2>>"$WFLOG" &
    sr_pid=$!
    sleep 0.3
    if ! alive "$sr_pid"; then
        rm -f "$sr_file"
        sr_err=$(tail -n 3 "$WFLOG" 2>/dev/null)
        notify_failed "${sr_err:-wf-recorder exited}"
        return 0
    fi
    state_write phase=recording "pid=$sr_pid" "file=$sr_file" "final=$sr_final" \
        "format=$sr_format" "target=$sr_target" "started=$(date +%s)"
    notify_recording "$sr_target" "$sr_final"
}

# finish_recording — runs in the background stop subshell: waits for
# wf-recorder to finalize, converts or moves the file, then delivers it.
finish_recording() {
    fr_self=$(exec sh -c 'echo "$PPID"')
    fr_n=0
    while alive "$st_pid" && [ "$fr_n" -lt 50 ]; do
        sleep 0.1
        fr_n=$((fr_n + 1))
    done
    if alive "$st_pid"; then
        kill -TERM "$st_pid" 2>/dev/null
        fr_n=0
        while alive "$st_pid" && [ "$fr_n" -lt 10 ]; do
            sleep 0.1
            fr_n=$((fr_n + 1))
        done
    fi

    # The loop writes this subshell's pid to the state right after forking
    # it; wait for that before clearing so the write cannot resurrect it.
    fr_n=0
    while [ "$(state_get pid)" != "$fr_self" ] && [ "$fr_n" -lt 40 ]; do
        sleep 0.05
        fr_n=$((fr_n + 1))
    done

    if ! [ -s "$st_file" ]; then
        rm -f "$st_file"
        state_clear
        notify_failed "no output written"
        return 0
    fi

    fr_name=${st_final##*/}
    fr_name=${fr_name%.*}
    fr_gif_failed=""
    if [ "$st_format" = gif ]; then
        notify_converting "$st_final"
        fr_vf="fps=$GIF_FPS"
        [ -n "$GIF_WIDTH" ] && fr_vf="$fr_vf,scale=$GIF_WIDTH:-1:flags=lanczos"
        fr_vf="$fr_vf,split[s0][s1];[s0]palettegen[p];[s1][p]paletteuse"
        if spawn ffmpeg -y -i "$st_file" -vf "$fr_vf" "$st_final" </dev/null >/dev/null 2>>"$WFLOG"; then
            rm -f "$st_file"
        else
            rm -f "$st_final"
            st_final="$OUTPUT_DIR/$fr_name.mp4"
            mv -f "$st_file" "$st_final"
            fr_gif_failed=1
        fi
    elif ! mv -f "$st_file" "$st_final"; then
        state_clear
        notify_failed "could not move ${st_file##*/} to $OUTPUT_DIR"
        return 0
    fi

    [ "$COPY_TO_CLIPBOARD" = true ] && copy_uri "$st_final"
    [ "$AUTOPLAY" = true ] && play_file "$st_final"
    state_clear
    if [ -n "$fr_gif_failed" ]; then
        notify_failed "GIF conversion failed, kept MP4"
    else
        notify_saved "$st_final"
    fi
}

# stop_recording — signals wf-recorder and hands the rest to a background
# subshell, so a long GIF conversion never stalls queries.
stop_recording() {
    refresh_phase
    [ "$PHASE" = recording ] || return 0
    st_pid=$(state_get pid)
    st_file=$(state_get file)
    st_final=$(state_get final)
    st_format=$(state_get format)
    st_target=$(state_get target)
    st_started=$(state_get started)
    kill -INT "$st_pid" 2>/dev/null
    # stdout is inherited on purpose: its notify lines are single printfs,
    # and every tool it runs has its own output redirected.
    finish_recording </dev/null &
    state_write phase=finishing "pid=$!" "file=$st_file" "final=$st_final" \
        "format=$st_format" "target=$st_target" "started=$st_started"
}

# --- delivery ----------------------------------------------------------------------

# file_uri <path> — file:// URI with % and spaces percent-encoded.
file_uri() {
    printf 'file://%s' "$(printf '%s' "$1" | sed -e 's/%/%25/g' -e 's/ /%20/g')"
}

# copy_uri <path> — wl-copy forks to serve the offer and returns at once.
copy_uri() {
    printf '%s\n' "$(file_uri "$1")" | spawn wl-copy -t text/uri-list >/dev/null 2>&1
}

play_file() {
    # shellcheck disable=SC2086 # MPV_ARGS is word-split on purpose.
    spawn mpv $MPV_ARGS -- "$1" </dev/null >/dev/null 2>&1 &
}

open_folder() {
    mkdir -p "$OUTPUT_DIR"
    spawn xdg-open "$OUTPUT_DIR" </dev/null >/dev/null 2>&1 &
}

# clear_recordings <confirm> — deletes the listed recordings once the user
# typed "delete"; never while a recording is running or finishing.
clear_recordings() {
    refresh_phase
    if [ -n "$PHASE" ]; then
        notify_info "Stop the recording first" ""
        return 0
    fi
    if [ "$(lower "$1")" != delete ]; then
        notify_info "Nothing deleted" "Type delete to confirm"
        return 0
    fi
    cr_files=$(list_recordings)
    cr_n=0
    if [ -n "$cr_files" ]; then
        cr_n=$(printf '%s\n' "$cr_files" | grep -c .)
        printf '%s\n' "$cr_files" | while IFS= read -r cr_f; do
            rm -f -- "$cr_f"
        done
    fi
    notify_info "Deleted $cr_n recordings" "$OUTPUT_DIR"
}

# handle_activate <id> — callback rows; deps and finishing are inert.
handle_activate() {
    case $1 in
        stop)
            stop_recording
            ;;
        recent:*)
            ha_name=${1#recent:}
            case $ha_name in
                '' | */*) return 0 ;;
            esac
            [ -f "$OUTPUT_DIR/$ha_name" ] || return 0
            [ "$COPY_TO_CLIPBOARD" = true ] && copy_uri "$OUTPUT_DIR/$ha_name"
            [ "$AUTOPLAY" = true ] && play_file "$OUTPUT_DIR/$ha_name"
            ;;
    esac
    return 0
}

# --- protocol loop ------------------------------------------------------------------

refresh_phase

while IFS= read -r line; do
    case "$line" in
        '') continue ;;
    esac
    event=$(str_field "$line" event)
    case "$event" in
        query)
            handle_query "$(num_field "$line" seq)" "$(str_field "$line" query)"
            ;;
        activate)
            handle_activate "$(str_field "$line" id)"
            ;;
        submit)
            id=$(str_field "$line" id)
            case $id in
                rec:*)
                    start_recording "${id#rec:}" "$(str_field "$line" format)" "$(str_field "$line" audio)"
                    ;;
                clear)
                    clear_recordings "$(str_field "$line" confirm)"
                    ;;
            esac
            ;;
        notify-action)
            if [ "$(str_field "$line" id)" = "$NOTIFY_ID" ]; then
                case $(str_field "$line" action) in
                    stop) stop_recording ;;
                    default) open_folder ;;
                esac
            fi
            ;;
        shutdown)
            # State and wf-recorder are left alone: a recording outlives this
            # process, and the next instance picks it up from the state file.
            exit 0
            ;;
        *)
            ;;
    esac
done
exit 0
