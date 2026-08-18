#!/bin/sh
# banshee claude-code plugin hook — forwards a Claude Code hook event to the
# plugin's FIFO. Register it in ~/.claude/settings.json:
#
#   {
#     "hooks": {
#       "PermissionRequest": [
#         { "hooks": [ { "type": "command",
#             "command": "~/.config/banshee/plugins/claude-code/hook.sh" } ] }
#       ],
#       "Notification": [
#         { "hooks": [ { "type": "command",
#             "command": "~/.config/banshee/plugins/claude-code/hook.sh" } ] }
#       ],
#       "Stop": [
#         { "hooks": [ { "type": "command",
#             "command": "~/.config/banshee/plugins/claude-code/hook.sh" } ] }
#       ]
#     }
#   }
#
# PermissionRequest fires immediately for tool approvals and plan approval;
# Notification covers the delayed prompts (idle, ~6 s permission reminder).
# This script never writes stdout, so registering it under PermissionRequest
# emits no permission decision — the prompt behaves exactly as without it.
#
# Claude Code passes the hook event as JSON on stdin. This script appends the
# hook's parent pid (so a notification click can focus the terminal running
# Claude) and writes one line to the FIFO through timeout(1), so a wedged or
# absent plugin can never hang Claude Code. Always exits 0 — a notification
# is never worth failing a hook over.
set -u

FIFO="${XDG_RUNTIME_DIR:-/tmp}/banshee/claude-code.fifo"
[ -p "$FIFO" ] || exit 0

raw=$(cat 2>/dev/null | tr '\n' ' ')
[ -n "$raw" ] || exit 0

payload=""
if command -v jq >/dev/null 2>&1; then
    payload=$(printf '%s' "$raw" | jq -c ". + {ppid: $PPID}" 2>/dev/null) || payload=""
fi
if [ -z "$payload" ]; then
    # No jq: splice the pid in before the closing brace.
    payload=$(printf '%s' "$raw" | sed "s/}[[:space:]]*\$/,\"ppid\":$PPID}/")
fi

printf '%s\n' "$payload" | timeout 2 sh -c "cat > \"$FIFO\"" 2>/dev/null || :
exit 0
