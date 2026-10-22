#!/bin/sh
# banshee passgen plugin — generate a password, UUIDv4, JWT secret or random
# key and copy it to the clipboard. Type "pas"/"pass", "gen" or "key" and pick
# a generator in the form; "key" preselects Key.
#
# Requires: coreutils (od, tr, head, base64, sed) and wl-clipboard (wl-copy).
# Optional: setsid from util-linux (keeps the copy alive across a reload).
# The generated value only ever travels through pipes: never argv, stdout or
# a log. Protocol reference: internal/providers/plugins/proto.go.
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
    printf ']}}]}\n'
}

# --- generators (each prints the value to its stdout, which is captured) ----

# gen_text <len> <symbols> <ambiguous> — rejection sampling via tr -dc, so no
# modulo bias. '-', '[' and ']' are octal-escaped so tr reads them literally.
gen_text() {
    if [ "$3" = on ]; then
        cs='A-HJ-NP-Za-km-z2-9'
    else
        cs='A-Za-z0-9'
    fi
    [ "$2" = on ] && cs="$cs"'!@#$%^&*()\055_=+\133\135{};:,.<>?/'
    LC_ALL=C tr -dc "$cs" </dev/urandom | head -c "$1"
}

gen_uuid() {
    # shellcheck disable=SC2046
    set -- $(od -An -tx1 -N16 /dev/urandom)
    b6="4${7#?}"
    b8=$(printf '%02x' $(((0x$9 & 0x3f) | 0x80)))
    printf '%s%s%s%s-%s%s-%s%s-%s%s-%s%s%s%s%s%s' \
        "$1" "$2" "$3" "$4" "$5" "$6" "$b6" "$8" "$b8" "${10}" \
        "${11}" "${12}" "${13}" "${14}" "${15}" "${16}"
}

# gen_bytes <bits> <encoding> — random bytes as hex, base64 or base64url.
gen_bytes() {
    n=$(($1 / 8))
    case "$2" in
        base64) head -c "$n" /dev/urandom | base64 | tr -d '\n' ;;
        base64url) head -c "$n" /dev/urandom | base64 | tr -d '\n=' | tr '+/' '-_' ;;
        *) od -An -tx1 -N"$n" /dev/urandom | tr -d ' \n' ;;
    esac
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

# handle_submit <line> — an older banshee renders sliders and checkboxes as
# text entries and ignores show_when, so every value is validated here.
handle_submit() {
    if ! command -v wl-copy >/dev/null 2>&1; then
        notify "wl-copy not found"
        return
    fi
    case "$(str_field "$1" generator)" in
        UUIDv4)
            summary="UUID copied"
            out=$(gen_uuid)
            ;;
        "JWT secret")
            summary="JWT secret copied"
            bits=$(one_of "$(str_field "$1" jwt_bits)" 256 256 512)
            enc=$(one_of "$(str_field "$1" jwt_encoding)" base64url base64url base64 hex)
            out=$(gen_bytes "$bits" "$enc")
            ;;
        Key)
            summary="Key copied"
            bits=$(one_of "$(str_field "$1" key_bits)" 256 256 128 512)
            enc=$(one_of "$(str_field "$1" key_encoding)" hex hex base64 base64url)
            out=$(gen_bytes "$bits" "$enc")
            ;;
        *)
            summary="Password copied"
            len=$(str_field "$1" length)
            case "$len" in
                '' | *[!0-9]*) len=24 ;;
            esac
            # Strip leading zeros so a value like "08" is not read as octal.
            len=$(printf '%s' "$len" | sed 's/^0*//')
            [ -z "$len" ] && len=8
            [ "${#len}" -gt 3 ] && len=128
            [ "$len" -lt 8 ] && len=8
            [ "$len" -gt 128 ] && len=128
            sym=on
            [ "$(str_field "$1" symbols)" = false ] && sym=off
            amb=off
            [ "$(str_field "$1" ambiguous)" = true ] && amb=on
            out=$(gen_text "$len" "$sym" "$amb")
            ;;
    esac
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
