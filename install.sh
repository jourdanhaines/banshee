#!/usr/bin/env bash
# banshee installer
# https://github.com/jourdanhaines/banshee
set -euo pipefail

BANSHEE_REPO="jourdanhaines/banshee"
BANSHEE_BRANCH="main"
BANSHEE_RAW_URL="https://raw.githubusercontent.com/${BANSHEE_REPO}/${BANSHEE_BRANCH}"
BANSHEE_INSTALL_DIR="${HOME}/.local/share/banshee/plugin"
BANSHEE_CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/banshee"
BANSHEE_BIN_DIR="${HOME}/.local/bin"

info() { echo -e "\033[1;34m::\033[0m $*"; }
ok()   { echo -e "\033[1;32m✓\033[0m $*"; }
warn() { echo -e "\033[1;33m!\033[0m $*"; }
err()  { echo -e "\033[1;31m✗\033[0m $*" >&2; }

# --- Check dependencies ---
check_deps() {
    local missing=()
    command -v fzf &>/dev/null || missing+=("fzf")
    command -v git &>/dev/null || missing+=("git")
    command -v curl &>/dev/null || { err "curl is required for remote install"; exit 1; }

    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing required dependencies: ${missing[*]}"
        echo "  Install them with your package manager, e.g.:"
        echo "    sudo pacman -S ${missing[*]}"
        echo "    sudo apt install ${missing[*]}"
        exit 1
    fi

    command -v tmux &>/dev/null || warn "tmux not found — session management will be disabled"
    command -v fd &>/dev/null   || warn "fd not found — falling back to find (slower)"
}

# --- Resolve script directory (for local installs) ---
BANSHEE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"

# --- Fetch a file (local copy preferred, fallback to GitHub) ---
download() {
    local file="$1" dest="$2"
    if [[ -f "$BANSHEE_SCRIPT_DIR/$file" ]]; then
        cp "$BANSHEE_SCRIPT_DIR/$file" "$dest"
    else
        curl -fsSL "${BANSHEE_RAW_URL}/${file}" -o "$dest" || {
            err "Failed to download $file"
            exit 1
        }
    fi
}

# --- Install files ---
install_files() {
    info "Installing banshee to $BANSHEE_INSTALL_DIR"
    mkdir -p "$BANSHEE_INSTALL_DIR" "$BANSHEE_BIN_DIR"

    download "banshee.sh"          "$BANSHEE_INSTALL_DIR/banshee.sh"
    download "banshee.plugin.zsh"  "$BANSHEE_INSTALL_DIR/banshee.plugin.zsh"
    download "banshee.plugin.bash" "$BANSHEE_INSTALL_DIR/banshee.plugin.bash"
    chmod +x "$BANSHEE_INSTALL_DIR/banshee.sh"

    # Create a standalone executable symlink
    ln -sf "$BANSHEE_INSTALL_DIR/banshee.sh" "$BANSHEE_BIN_DIR/banshee"
    chmod +x "$BANSHEE_BIN_DIR/banshee"

    ok "Plugin files installed"
}

# --- Install config ---
install_config() {
    if [[ -f "$BANSHEE_CONFIG_DIR/banshee.conf" ]]; then
        warn "Config already exists at $BANSHEE_CONFIG_DIR/banshee.conf — skipping"
    else
        mkdir -p "$BANSHEE_CONFIG_DIR"
        download "banshee.conf" "$BANSHEE_CONFIG_DIR/banshee.conf"
        ok "Default config installed to $BANSHEE_CONFIG_DIR/banshee.conf"
    fi
}

# --- Add source line to shell config ---
setup_shell() {
    local shell_name
    shell_name=$(basename "${SHELL:-bash}")

    local rc_file plugin_file source_line
    case "$shell_name" in
        zsh)
            rc_file="$HOME/.zshrc"
            plugin_file="$BANSHEE_INSTALL_DIR/banshee.plugin.zsh"
            ;;
        *)
            rc_file="$HOME/.bashrc"
            plugin_file="$BANSHEE_INSTALL_DIR/banshee.plugin.bash"
            ;;
    esac

    source_line="source \"$plugin_file\""

    # Already present — skip
    if [[ -f "$rc_file" ]] && grep -qF "$plugin_file" "$rc_file"; then
        ok "Shell config already set up in $rc_file"
        return
    fi

    echo "" >> "$rc_file"
    echo "# banshee - git repo switcher" >> "$rc_file"
    echo "$source_line" >> "$rc_file"
    ok "Added source line to $rc_file"

    echo ""
    info "Restart your shell or run: source $rc_file"
}

# --- Uninstall ---
uninstall() {
    info "Uninstalling banshee..."
    rm -rf "$BANSHEE_INSTALL_DIR"
    rm -f "$BANSHEE_BIN_DIR/banshee"

    # Remove source line from shell config
    for rc_file in "$HOME/.zshrc" "$HOME/.bashrc"; do
        if [[ -f "$rc_file" ]] && grep -qF "banshee" "$rc_file"; then
            sed -i '/# banshee - git repo switcher/d;/banshee\.plugin\./d' "$rc_file"
            ok "Removed banshee from $rc_file"
        fi
    done

    ok "banshee uninstalled"
    warn "Config left at $BANSHEE_CONFIG_DIR (remove manually if desired)"
}

# --- Main ---
case "${1:-}" in
    --uninstall)
        uninstall
        ;;
    *)
        echo "╔══════════════════════════════════╗"
        echo "║     banshee installer            ║"
        echo "╚══════════════════════════════════╝"
        echo ""
        check_deps
        install_files
        install_config
        setup_shell
        ok "Installation complete!"
        ;;
esac
