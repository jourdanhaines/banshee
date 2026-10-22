#!/bin/sh
# banshee passgen plugin — generate a password, UUIDv4, JWT secret or random
# key and copy it to the clipboard. Type "pas"/"pass", "gen" or "key" and pick
# a generator in the form; "key" preselects Key.
#
# banshee generates the value itself (one "generate" field per variant, shown
# by show_when); this script only copies the one it receives.
#
# Requires: sed and wl-clipboard (wl-copy). Optional: setsid from util-linux
# (keeps the copy alive across a reload). The value only ever travels through
# pipes: never argv, stdout or a log. Protocol: internal/providers/plugins/proto.go.
set -u

HAVE_SETSID=""
command -v setsid >/dev/null 2>&1 && HAVE_SETSID=1

# --- tiny JSON helpers (same approach as plugins/example) --------------------

str_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p"
}

num_field() {
    printf '%s' "$1" | sed -n "s/.*\"$2\"[[:space:]]*:[[:space:]]*\\([0-9][0-9]*\\).*/\\1/p"
}

# --- query ------------------------------------------------------------------

# emit_form <seq> <prefix> — the single generator row. The generator dropdown
# preselects its first option, so "key" leads with Key.
emit_form() {
    if [ "$2" = key ]; then
        title="Generate key"
        gens='"Key","Text password","UUIDv4","JWT secret"'
    else
        title="Generate password"
        gens='"Text password","UUIDv4","JWT secret","Key"'
    fi
    text='"show_when":[{"key":"generator","values":["Text password"]}]'
    jwt='"show_when":[{"key":"generator","values":["JWT secret"]}]'
    key='"show_when":[{"key":"generator","values":["Key"]}]'
    printf '{"v":1,"seq":%s,"event":"results","done":true,"results":[' "$1"
    printf '{"id":"gen","title":"%s","subtitle":"Password, UUID, JWT secret or key to the clipboard","action":{"kind":"callback"},' "$title"
    printf '"form":{"title":"Generate","submit_label":"Generate","fields":['
    printf '{"key":"generator","label":"Generator","options":[%s]},' "$gens"
    printf '{"key":"length","label":"Length","slider":{"min":8,"max":128,"step":1,"default":24},%s},' "$text"
    printf '{"key":"symbols","label":"Symbols","checkbox":true,"checked":true,%s},' "$text"
    printf '{"key":"ambiguous","label":"Exclude ambiguous (0O1lI|)","checkbox":true,%s},' "$text"
    printf '{"key":"jwt_bits","label":"Bits","options":["256","512"],%s},' "$jwt"
    printf '{"key":"jwt_encoding","label":"Encoding","options":["base64url","base64","hex"],%s},' "$jwt"
    printf '{"key":"key_bits","label":"Bits","options":["256","128","512"],%s},' "$key"
    printf '{"key":"key_encoding","label":"Encoding","options":["hex","base64","base64url"],%s}' "$key"
    emit_generated
    printf ']}}]}\n'
}

ALNUM=ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789
ALNUM_UNAMBIGUOUS=ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789
SYMBOLS='!@#$%^&*()-_=+[]{};:,.<>?/'

# gen_field <key> <generate-json> <show_when-json> — one unlabelled generated
# field; specs go through %s because the symbol set contains '%'.
gen_field() {
    printf ',{"key":"%s","label":"","generate":%s,"show_when":%s}' "$1" "$2" "$3"
}

# cond <key> <value> — one show_when condition.
cond() {
    printf '{"key":"%s","values":["%s"]}' "$1" "$2"
}

# emit_generated — every variant the submit side can expect, one per
# combination of the options that shape the value.
emit_generated() {
    for s in on off; do
        for a in on off; do
            cs=$ALNUM
            [ "$a" = on ] && cs=$ALNUM_UNAMBIGUOUS
            [ "$s" = on ] && cs="$cs$SYMBOLS"
            sv=false am=false
            [ "$s" = on ] && sv=true
            [ "$a" = on ] && am=true
            gen_field "gen_text_${s}_$a" \
                "{\"charset\":\"$cs\",\"length_from\":\"length\"}" \
                "[$(cond generator 'Text password'),$(cond symbols $sv),$(cond ambiguous $am)]"
        done
    done
    gen_field gen_uuid \
        '{"pattern":"xxxxxxxx-xxxx-4xxx-[89ab]xxx-xxxxxxxxxxxx","charset":"0123456789abcdef"}' \
        "[$(cond generator UUIDv4)]"
    for bits in 256 512; do
        for enc in base64url base64 hex; do
            gen_field "gen_jwt_${bits}_$enc" "{\"bytes\":$((bits / 8)),\"encoding\":\"$enc\"}" \
                "[$(cond generator 'JWT secret'),$(cond jwt_bits "$bits"),$(cond jwt_encoding "$enc")]"
        done
    done
    for bits in 256 128 512; do
        for enc in hex base64 base64url; do
            gen_field "gen_key_${bits}_$enc" "{\"bytes\":$((bits / 8)),\"encoding\":\"$enc\"}" \
                "[$(cond generator Key),$(cond key_bits "$bits"),$(cond key_encoding "$enc")]"
        done
    done
}

# --- submit -----------------------------------------------------------------

notify() {
    printf '{"v":1,"event":"notify","notify":{"id":"passgen:copied","summary":"%s","body":"","icon":"dialog-password-symbolic","timeout_ms":3000,"actions":[]}}\n' "$1"
}

# one_of <value> <default> <allowed>... — value when allowed, else default.
one_of() {
    oo_v=$1 oo_d=$2
    shift 2
    for oo_a; do
        [ "$oo_v" = "$oo_a" ] && { printf '%s' "$oo_v"; return; }
    done
    printf '%s' "$oo_d"
}

# secret_field <line> <key> — a generated value, JSON-unescaped. Safe with sed
# because no charset contains '"' or '\'; the only escapes on the wire are
# Go's HTML-safe \u003c \u003e \u0026. The value moves only through pipes.
secret_field() {
    str_field "$1" "$2" | sed 's/\\u003c/</g; s/\\u003e/>/g; s/\\u0026/\&/g'
}

# any_generated <line> — the leftmost non-empty gen_* value, for a field set
# that disagrees with the expected key (e.g. a newer form layout).
any_generated() {
    printf '%s' "$1" |
        sed -n 's/"gen_[a-z0-9_]*"[[:space:]]*:[[:space:]]*"\([^"][^"]*\)".*/"gen_:"\1/; s/.*"gen_:"//p' |
        sed 's/\\u003c/</g; s/\\u003e/>/g; s/\\u0026/\&/g'
}

# handle_submit <line> — picks the generated field matching the chosen options.
# An older banshee ignores show_when, so the options are re-validated here, and
# it cannot generate, so an empty value copies nothing.
handle_submit() {
    if ! command -v wl-copy >/dev/null 2>&1; then
        notify "wl-copy not found"
        return
    fi
    case "$(str_field "$1" generator)" in
        UUIDv4)
            summary="UUID copied"
            want=gen_uuid
            ;;
        "JWT secret")
            summary="JWT secret copied"
            bits=$(one_of "$(str_field "$1" jwt_bits)" 256 256 512)
            enc=$(one_of "$(str_field "$1" jwt_encoding)" base64url base64url base64 hex)
            want="gen_jwt_${bits}_$enc"
            ;;
        Key)
            summary="Key copied"
            bits=$(one_of "$(str_field "$1" key_bits)" 256 256 128 512)
            enc=$(one_of "$(str_field "$1" key_encoding)" hex hex base64 base64url)
            want="gen_key_${bits}_$enc"
            ;;
        *)
            summary="Password copied"
            sym=on
            [ "$(str_field "$1" symbols)" = false ] && sym=off
            amb=off
            [ "$(str_field "$1" ambiguous)" = true ] && amb=on
            want="gen_text_${sym}_$amb"
            ;;
    esac
    out=$(secret_field "$1" "$want")
    [ -z "$out" ] && out=$(any_generated "$1")
    if [ -z "$out" ]; then
        notify "Update banshee to generate"
        return
    fi
    # wl-copy forks to serve the offer; its own session survives the host's
    # process-group kill on reload/shutdown.
    if [ -n "$HAVE_SETSID" ]; then
        printf '%s' "$out" | setsid wl-copy --sensitive >/dev/null 2>&1
    else
        printf '%s' "$out" | wl-copy --sensitive >/dev/null 2>&1
    fi
    out=""
    notify "$summary"
}

# --- event loop -------------------------------------------------------------

while IFS= read -r line; do
    case "$(str_field "$line" event)" in
        query) emit_form "$(num_field "$line" seq)" "$(str_field "$line" prefix)" ;;
        submit)
            [ "$(str_field "$line" id)" = gen ] && handle_submit "$line"
            ;;
        shutdown) exit 0 ;;
    esac
done
