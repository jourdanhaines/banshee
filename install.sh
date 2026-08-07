#!/usr/bin/env bash
# banshee bootstrap installer
# https://github.com/jourdanhaines/banshee
#
#   ./install.sh                 build + install + wire up Hyprland and the shell
#   ./install.sh --no-hyprland   skip the hyprland.conf edit
#   ./install.sh --no-shell      skip the .zshrc/.bashrc edit
#   ./install.sh --uninstall     undo everything (configs are kept)
#
# Every step is idempotent: running this twice changes nothing the second time.
set -euo pipefail

REPO_URL="https://github.com/jourdanhaines/banshee"
BIN_DIR="${HOME}/.local/bin"
SHARE_DIR="${HOME}/.local/share/banshee"
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/banshee"
HYPR_CONF="${XDG_CONFIG_HOME:-$HOME/.config}/hypr/hyprland.conf"
SYSTEMD_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

DO_HYPRLAND=1
DO_SHELL=1

# SHELL_MARKER is the comment setup_shell writes above the source line. It is
# the anchor uninstall deletes on, so the two must stay in sync.
SHELL_MARKER='# banshee — launcher + tmux sessions'

info() { printf '\033[1;34m::\033[0m %s\n' "$*"; }
ok() { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!\033[0m %s\n' "$*"; }
err() { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; }

# ---------------------------------------------------------------------------
# Source tree
# ---------------------------------------------------------------------------
# Local checkout when run from one, otherwise clone into a temp dir so the
# curl-pipe install still works (it builds from source, so the first run takes
# a while — the gotk4 cgo tree is large).
SRC_DIR=""
CLONED=""

resolve_source() {
  local here
  here="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
  if [[ -n "$here" && -f "$here/go.mod" && -d "$here/cmd/banshee" ]]; then
    SRC_DIR="$here"
    info "Building from local checkout: $SRC_DIR"
    return
  fi

  command -v git >/dev/null || {
    err "git is required to fetch the sources"
    exit 1
  }
  CLONED="$(mktemp -d)"
  info "Cloning $REPO_URL"
  git clone --depth 1 "$REPO_URL" "$CLONED" >/dev/null 2>&1 || {
    err "clone failed"
    exit 1
  }
  SRC_DIR="$CLONED"
}

cleanup() { [[ -n "$CLONED" ]] && rm -rf "$CLONED"; }
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Dependencies
# ---------------------------------------------------------------------------
check_deps() {
  local missing=()

  command -v go >/dev/null || missing+=("go")
  command -v make >/dev/null || missing+=("make")
  command -v pkg-config >/dev/null || missing+=("pkgconf")
  pkg-config --exists gtk4 2>/dev/null || missing+=("gtk4")
  pkg-config --exists gtk4-layer-shell-0 2>/dev/null || missing+=("gtk4-layer-shell")

  if ((${#missing[@]})); then
    err "Missing build dependencies: ${missing[*]}"
    if command -v pacman >/dev/null; then
      echo "    sudo pacman -S --needed ${missing[*]}"
    else
      echo "    install them with your package manager"
    fi
    exit 1
  fi
  ok "Build dependencies present"

  # Runtime niceties — banshee works without them, with reduced features.
  command -v tmux >/dev/null || warn "tmux not found — session features are disabled"
  command -v fzf >/dev/null || warn "fzf not found — the CLI picker falls back to a numbered list"
  command -v git >/dev/null || warn "git not found — GitHub connector rows will not appear"
}

# ---------------------------------------------------------------------------
# Build + install
# ---------------------------------------------------------------------------
build_and_install() {
  info "Building banshee (first build compiles GTK bindings — this can take 5-15 minutes)"
  make -C "$SRC_DIR" build
  ok "Built $SRC_DIR/bin/banshee"

  info "Installing"
  make -C "$SRC_DIR" install
  ok "Installed to $BIN_DIR/banshee"

  case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "$BIN_DIR is not on your \$PATH — add it to your shell rc" ;;
  esac
}

# ---------------------------------------------------------------------------
# Hyprland
# ---------------------------------------------------------------------------
# Swaps the wofi $menu binding for banshee and adds the two layerrules that
# give the launcher its blurred glass panel. A timestamped backup is taken
# before the first edit.
setup_hyprland() {
  ((DO_HYPRLAND)) || {
    info "Skipping Hyprland setup (--no-hyprland)"
    return
  }

  if [[ ! -f "$HYPR_CONF" ]]; then
    warn "No $HYPR_CONF — add this yourself:"
    printf '      layerrule {\n          name = banshee\n          match:namespace = banshee\n          blur = on\n          ignore_alpha = 0\n      }\n      $menu = banshee toggle\n'
    return
  fi

  local changed=0
  local appended=0

  # append_line writes one line to the end of hyprland.conf, prefixing a blank
  # line and a comment header the first time so the additions are identifiable.
  append_line() {
    if ((!appended)); then
      printf '\n# banshee launcher — https://github.com/jourdanhaines/banshee\n' >>"$HYPR_CONF"
      appended=1
    fi
    printf '%s\n' "$1" >>"$HYPR_CONF"
  }

  # The absolute binary path is used because Hyprland's own PATH usually lacks
  # ~/.local/bin, which makes a bare `exec banshee toggle` fail silently.
  local menu_cmd="$BIN_DIR/banshee toggle"

  if ! grep -qE '^\s*\$menu\s*=\s*\S*banshee toggle\s*$' "$HYPR_CONF"; then
    backup_hyprland
    if grep -qE '^\s*\$menu\s*=' "$HYPR_CONF"; then
      # Replace whatever $menu is currently bound to (wofi, rofi, ...), keeping
      # any existing `bind = $mainMod, SPACE, exec, $menu` working as-is.
      sed -i -E "s|^(\s*)\\\$menu\s*=.*\$|\1\$menu = $menu_cmd|" "$HYPR_CONF"
      ok "Rebound \$menu to '$menu_cmd'"
    else
      append_line "\$menu = $menu_cmd"
      ok "Added \$menu = $menu_cmd"
      warn "No key was bound to \$menu — add e.g. 'bind = \$mainMod, SPACE, exec, \$menu'"
    fi
    changed=1
  else
    ok "\$menu already bound to banshee"
  fi

  # Hyprland >= 0.53 block syntax; detected by the namespace match line so the
  # check survives reformatting inside the block.
  if grep -qE 'match:namespace\s*=\s*banshee' "$HYPR_CONF"; then
    ok "Present: banshee layerrule block"
  else
    backup_hyprland
    append_line 'layerrule {'
    append_line '    name = banshee'
    append_line '    match:namespace = banshee'
    append_line '    blur = on'
    append_line '    ignore_alpha = 0'
    append_line '}'
    ok "Added: banshee layerrule block (blur + ignore_alpha)"
    changed=1
  fi

  if ((changed)) && command -v hyprctl >/dev/null; then
    hyprctl reload >/dev/null 2>&1 && ok "Reloaded Hyprland" || true
  fi
}

# backup_hyprland takes one timestamped copy per install run, before the first
# modification. Subsequent calls in the same run are no-ops.
HYPR_BACKED_UP=0
backup_hyprland() {
  ((HYPR_BACKED_UP)) && return
  local stamp backup
  stamp="$(date +%Y%m%d-%H%M%S)"
  backup="${HYPR_CONF}.banshee-backup-${stamp}"
  cp -p "$HYPR_CONF" "$backup"
  ok "Backed up hyprland.conf → $backup"
  HYPR_BACKED_UP=1
}

# ---------------------------------------------------------------------------
# Shell integration
# ---------------------------------------------------------------------------
setup_shell() {
  ((DO_SHELL)) || {
    info "Skipping shell setup (--no-shell)"
    return
  }

  local rc plugin
  case "$(basename "${SHELL:-bash}")" in
  zsh)
    rc="$HOME/.zshrc"
    plugin="$SHARE_DIR/banshee.plugin.zsh"
    ;;
  *)
    rc="$HOME/.bashrc"
    plugin="$SHARE_DIR/banshee.plugin.bash"
    ;;
  esac

  if [[ -f "$rc" ]] && grep -qF "$plugin" "$rc"; then
    ok "Shell integration already in $rc"
    return
  fi

  {
    printf '\n%s\n' "$SHELL_MARKER"
    printf '[ -f "%s" ] && source "%s"\n' "$plugin" "$plugin"
  } >>"$rc"
  ok "Added shell integration to $rc"
  info "Restart your shell or run: source $rc"
}

# ---------------------------------------------------------------------------
# Uninstall
# ---------------------------------------------------------------------------
uninstall() {
  info "Uninstalling banshee"

  "$BIN_DIR/banshee" quit >/dev/null 2>&1 || true
  rm -f "$BIN_DIR/banshee"
  rm -f "$SHARE_DIR/banshee.plugin.zsh" "$SHARE_DIR/banshee.plugin.bash"
  rm -f "$SYSTEMD_DIR/banshee.service"
  rmdir "$SHARE_DIR" 2>/dev/null || true

  local rc
  for rc in "$HOME/.zshrc" "$HOME/.bashrc"; do
    [[ -f "$rc" ]] || continue
    grep -qF "$SHELL_MARKER" "$rc" || continue
    # Back up first: a shell rc is the file most likely to hold irreplaceable
    # hand-written configuration, and it gets the same treatment hyprland.conf
    # does. Then delete only the exact two-line block setup_shell appended
    # (the marker comment and the source line right after it) — a broader
    # pattern would eat a user's own `source .../banshee.plugin.zsh` line.
    local stamp backup
    stamp="$(date +%Y%m%d-%H%M%S)"
    backup="${rc}.banshee-backup-${stamp}"
    cp -p "$rc" "$backup"
    ok "Backed up $rc → $backup"
    sed -i "\|^${SHELL_MARKER}\$|{N;d;}" "$rc"
    ok "Removed shell integration from $rc"
  done

  warn "hyprland.conf was left untouched — remove the banshee \$menu bind and layerrules by hand"
  warn "Configs left at $CONFIG_DIR"
  ok "banshee uninstalled"
}

# ---------------------------------------------------------------------------
main() {
  while (($#)); do
    case "$1" in
    --uninstall)
      uninstall
      exit 0
      ;;
    --no-hyprland) DO_HYPRLAND=0 ;;
    --no-shell) DO_SHELL=0 ;;
    -h | --help)
      sed -n '2,10p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      err "unknown option: $1"
      exit 1
      ;;
    esac
    shift
  done

  echo "banshee installer"
  echo
  resolve_source
  check_deps
  build_and_install
  setup_hyprland
  setup_shell

  echo
  ok "Done. Run 'banshee doctor' to verify, then press \$mainMod+SPACE (or your \$menu bind)."
}

main "$@"
