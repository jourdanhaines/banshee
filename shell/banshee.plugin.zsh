#!/usr/bin/env zsh
# banshee — zsh integration
# https://github.com/jourdanhaines/banshee
#
# All logic lives in the banshee binary. This file only provides the four
# things a binary cannot do for itself:
#
#   1. cd'ing the *current* shell when tmux is not installed
#   2. a keybind widget that runs banshee in the current terminal
#   3. tab completion, fed by `banshee _complete <kind>`
#   4. the interactive startup prompt, via `banshee _startup-prompt`
#
# Source it from ~/.zshrc:
#     source ~/.local/share/banshee/banshee.plugin.zsh

(( $+commands[banshee] )) || return 0

# --- Shell wrapper -----------------------------------------------------------
# Without tmux there is no session to attach to, so a bare `banshee [query]`
# degrades to a fuzzy repo jumper — which only works from a shell function,
# because a binary cannot change its parent's working directory. `banshee
# _pick-repo` runs the same picker and prints the chosen path; an empty answer
# (the user pressed Esc) leaves the shell where it was. Everything else, and
# every flag or launcher verb, goes straight to the binary.
banshee() {
  case "${1:-}" in
    -*|toggle|show|hide|daemon|reload|quit|doctor|_*)
      command banshee "$@"
      return $?
      ;;
  esac
  if (( $+commands[tmux] )); then
    command banshee "$@"
    return $?
  fi
  local selected
  selected="$(command banshee _pick-repo "${1:-}")" || return $?
  [[ -n "$selected" ]] || return 0
  builtin cd -- "$selected"
}

# --- Keybinding widget -------------------------------------------------------
# banshee replaces the whole line: run it, then redraw the prompt.
_banshee_widget() {
  zle -I
  banshee
  zle reset-prompt
}
zle -N _banshee_widget

# keybind is read straight out of banshee.conf so the binary stays the single
# source of truth for configuration.
#
# extended_glob is required for the `#` (zero-or-more) operator in the match
# below and is *off* by default in zsh; local_options restores whatever the
# user had when the function returns.
_banshee_keybind() {
  setopt local_options extended_glob
  local conf="${XDG_CONFIG_HOME:-$HOME/.config}/banshee/banshee.conf" line
  local bind=ctrl-f
  if [[ -r "$conf" ]]; then
    while IFS= read -r line; do
      [[ "$line" == [[:space:]]#keybind[[:space:]]#=* ]] || continue
      bind="${${line#*=}## }"
      bind="${bind%% }"
      break
    done < "$conf"
  fi
  print -r -- "$bind"
}

case "$(_banshee_keybind)" in
  ctrl-f)   bindkey '^f'  _banshee_widget ;;
  ctrl-g)   bindkey '^g'  _banshee_widget ;;
  ctrl-b)   bindkey '^b'  _banshee_widget ;;
  ctrl-p)   bindkey '^p'  _banshee_widget ;;
  ctrl-o)   bindkey '^o'  _banshee_widget ;;
  'ctrl-\') bindkey '^\'  _banshee_widget ;;
  *)        bindkey "$(_banshee_keybind)" _banshee_widget 2>/dev/null || true ;;
esac

# --- Completion --------------------------------------------------------------
# `banshee _complete <kind>` prints one sorted, unique candidate per line.
_banshee_complete() {
  local prev="${words[CURRENT-1]:-}"
  local -a names

  case "$prev" in
    -s|--session|-se|--edit-session)
      names=("${(@f)$(banshee _complete pool 2>/dev/null)}")
      _describe 'session targets' names
      return
      ;;
    -g|--group|-ge|--edit-group)
      names=("${(@f)$(banshee _complete groups 2>/dev/null)}")
      _describe 'groups' names
      return
      ;;
    toggle|show)
      return  # free-form query
      ;;
  esac

  if [[ "${words[CURRENT]}" == -* ]]; then
    _describe 'options' '(
      -s:load\ target\ \(create\ config\ if\ missing\)
      -se:edit\ target\ session\ config
      -g:load\ group
      -ge:edit\ group
      -r:re-run\ last\ action
      -l:list\ configs\ and\ groups
      -c:clear\ repo\ cache
      -v:version
      -h:help
    )'
    return
  fi

  names=("${(@f)$(banshee _complete pool 2>/dev/null)}")
  _describe 'targets' names

  local -a verbs
  verbs=(toggle show hide daemon reload quit doctor)
  _describe 'launcher commands' verbs
}

compdef _banshee_complete banshee

# --- Startup restore prompt --------------------------------------------------
# The binary self-guards on tmux presence, `startup_prompt`, $TMUX, tty-ness and
# $BANSHEE_STARTUP_CHECKED; the shell only has to mark the session as checked so
# nested shells stay quiet.
if [[ -o interactive && -z "${BANSHEE_STARTUP_CHECKED:-}" ]]; then
  banshee _startup-prompt
  export BANSHEE_STARTUP_CHECKED=1
fi
