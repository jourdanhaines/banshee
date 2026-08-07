#!/usr/bin/env bash
# banshee — bash integration
# https://github.com/jourdanhaines/banshee
#
# All logic lives in the banshee binary. This file only provides the four
# things a binary cannot do for itself:
#
#   1. cd'ing the *current* shell when tmux is not installed
#   2. a keybind that runs banshee in the current terminal
#   3. tab completion, fed by `banshee _complete <kind>`
#   4. the interactive startup prompt, via `banshee _startup-prompt`
#
# Source it from ~/.bashrc:
#     source ~/.local/share/banshee/banshee.plugin.bash

command -v banshee >/dev/null 2>&1 || return 0

# --- Shell wrapper -----------------------------------------------------------
# Without tmux there is no session to attach to, so a bare `banshee [query]`
# degrades to a fuzzy repo jumper — which only works from a shell function,
# because a binary cannot change its parent's working directory. `banshee
# _pick-repo` runs the same picker and prints the chosen path; an empty answer
# (the user pressed Esc) leaves the shell where it was. Everything else, and
# every flag or launcher verb, goes straight to the binary.
banshee() {
  case "${1:-}" in
  -* | toggle | show | hide | daemon | reload | quit | doctor | _*)
    command banshee "$@"
    return $?
    ;;
  esac
  if command -v tmux >/dev/null 2>&1; then
    command banshee "$@"
    return $?
  fi
  local selected
  selected="$(command banshee _pick-repo "${1:-}")" || return $?
  [[ -n "$selected" ]] || return 0
  builtin cd -- "$selected"
}

# --- Keybinding --------------------------------------------------------------
# keybind is read straight out of banshee.conf so the binary stays the single
# source of truth for configuration.
_banshee_read_keybind() {
  local conf="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/banshee.conf" line key
  key=ctrl-f
  if [[ -r "$conf" ]]; then
    while IFS= read -r line; do
      [[ "$line" =~ ^[[:space:]]*keybind[[:space:]]*= ]] || continue
      key="${line#*=}"
      key="${key## }"
      key="${key%% }"
      break
    done < "$conf"
  fi
  printf '%s' "$key"
}

_banshee_bind_key() {
  [[ $- == *i* ]] || return 0

  local seq
  case "$(_banshee_read_keybind)" in
    ctrl-f)   seq='\C-f' ;;
    ctrl-g)   seq='\C-g' ;;
    ctrl-b)   seq='\C-b' ;;
    ctrl-p)   seq='\C-p' ;;
    ctrl-o)   seq='\C-o' ;;
    'ctrl-\') seq='\C-\' ;;
    *)        seq="$(_banshee_read_keybind)" ;;
  esac
  bind -x "\"$seq\": banshee" 2>/dev/null || true
}
_banshee_bind_key

# --- Completion --------------------------------------------------------------
# `banshee _complete <kind>` prints one sorted, unique candidate per line.
_banshee_completions() {
  local cur="${COMP_WORDS[COMP_CWORD]}"
  local prev="${COMP_WORDS[COMP_CWORD - 1]:-}"

  case "$prev" in
    -s | --session | -se | --edit-session)
      mapfile -t COMPREPLY < <(compgen -W "$(banshee _complete pool 2>/dev/null)" -- "$cur")
      return
      ;;
    -g | --group | -ge | --edit-group)
      mapfile -t COMPREPLY < <(compgen -W "$(banshee _complete groups 2>/dev/null)" -- "$cur")
      return
      ;;
    toggle | show)
      return # free-form query
      ;;
  esac

  if [[ "$cur" == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W \
      "-s -se -g -ge -r -l -c -v -h --session --edit-session --group --edit-group --restore --list --clear --version --help" \
      -- "$cur")
    return
  fi

  mapfile -t COMPREPLY < <(compgen -W \
    "$(banshee _complete pool 2>/dev/null) toggle show hide daemon reload quit doctor" \
    -- "$cur")
}

complete -F _banshee_completions banshee

# --- Startup restore prompt --------------------------------------------------
# The binary self-guards on tmux presence, `startup_prompt`, $TMUX, tty-ness and
# $BANSHEE_STARTUP_CHECKED; the shell only has to mark the session as checked so
# nested shells stay quiet.
if [[ $- == *i* && -z "${BANSHEE_STARTUP_CHECKED:-}" ]]; then
  banshee _startup-prompt
  export BANSHEE_STARTUP_CHECKED=1
fi
