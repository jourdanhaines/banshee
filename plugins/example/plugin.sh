#!/bin/sh
# banshee example exec plugin — a reference implementation of the JSON Lines
# plugin protocol (v1). See internal/providers/plugins/proto.go for the full
# specification, and the Plugins section of CLAUDE.md for the manifest layout.
#
# Install:
#   cp -r plugins/example ~/.config/banshee/plugins/example
#   banshee reload
#
# Try it: open the launcher and type "demo".
#
# The manifest sets "prefix": "demo", so banshee only sends this plugin queries
# that start with "demo"; the prefix is stripped before it reaches us. The
# plugin stays alive for the lifetime of the daemon: read one JSON object per
# line from stdin, write one JSON object per line to stdout, never buffer.
set -u

# --- tiny JSON helpers (a real plugin should use a JSON library) -------------

# str_field <line> <key> — value of a string field.
str_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"
}

# num_field <line> <key> — value of a numeric field.
num_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\":\\([0-9]*\\).*/\\1/p"
}

# json_escape <text> — escape a value for embedding in a JSON string.
json_escape() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# --- protocol handlers ------------------------------------------------------

# emit_results <seq> <query> — answer one query event.
#
# Every message must echo the seq it answers: banshee discards results whose
# seq is not the query it is currently waiting for. Results may be split over
# several messages; the last one sets "done": true. Anything that arrives after
# the soft timeout (exec.timeout_ms, default 150ms) is dropped, so answer fast
# and stream partial results if the work is slow.
emit_results() {
    seq=$1
    query=$(json_escape "$2")

    printf '{"v":1,"seq":%s,"event":"results","done":true,"results":[' "$seq"

    # 1. A callback result: activating it sends an "activate" event back here.
    printf '{"id":"hello","title":"Hello from the example plugin",'
    printf '"subtitle":"you typed: %s","score":90,"action":{"kind":"callback"}}' "$query"

    # 2. A url result: banshee opens it with the system handler, no callback.
    printf ',{"id":"docs","title":"Read the banshee plugin docs",'
    printf '"subtitle":"internal/providers/plugins/proto.go","score":80,"icon":"text-x-generic-symbolic",'
    printf '"action":{"kind":"url","url":"https://github.com/jourdanhaines/banshee/blob/main/internal/providers/plugins/proto.go"}}'

    # 3. An exec-detach result: banshee runs argv detached from the daemon.
    printf ',{"id":"notify","title":"Send a test notification",'
    printf '"subtitle":"exec-detach demo","score":70,'
    printf '"action":{"kind":"exec-detach","argv":["notify-send","banshee example","it works"]}}'

    printf ']}\n'
}

# handle_activate <result-id> — a callback result was activated.
handle_activate() {
    case "$1" in
        hello)
            if command -v notify-send >/dev/null 2>&1; then
                notify-send "banshee example" "callback for '$1' received"
            else
                printf '%s activated\n' "$1" >>"${TMPDIR:-/tmp}/banshee-example.log"
            fi
            ;;
    esac
}

# --- event loop -------------------------------------------------------------

while IFS= read -r line; do
    case "$line" in
        '') continue ;;
    esac
    event=$(str_field "$line" event)
    case "$event" in
        query)
            emit_results "$(num_field "$line" seq)" "$(str_field "$line" query)"
            ;;
        activate)
            seq=$(num_field "$line" seq)
            handle_activate "$(str_field "$line" id)"
            # Acknowledgement is optional; banshee does not wait for it.
            printf '{"v":1,"seq":%s,"event":"activated"}\n' "$seq"
            ;;
        shutdown)
            exit 0
            ;;
        *)
            # Unknown events are ignored — new event kinds may be added.
            ;;
    esac
done
