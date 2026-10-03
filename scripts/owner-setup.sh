#!/usr/bin/env bash
# owner-setup.sh: walk the owner through the fleet-board setup steps only the
# owner can do. Run it on each fleet machine; every stage can be skipped and
# run again later. It never reads a harness's credentials or trust list.
#
#   scripts/owner-setup.sh            interactive walk-through (needs a terminal)
#   scripts/owner-setup.sh --check    show where each stage stands; changes nothing
#   scripts/owner-setup.sh --list     list the stages
#   scripts/owner-setup.sh --stage N  run only stage N
#
# Stages (plan section 3, docs/plan/v1-implementation-plan.md):
#   1 github-app      create and install the read-only GitHub App (#26, item 0.2)
#   2 muse-update     choose whether Muse updates itself on this machine (#28, 0.4)
#   3 probe-trust     trust the probe folder once in each harness (C3, #54)
#   4 mac-quarantine  clear the "downloaded from the internet" flag (after install)
#   5 linux-linger    keep fleet-agent running after logout (Linux workers)
#
# Works with the macOS system bash (3.2): no associative arrays.

set -u

REPO="linuxdynasty/fleet-board"
PROBE_DIR="${HOME}/.local/share/fleet-agent/probe"
HARNESSES="claude codex muse opencode"
BINARIES="fleetd fleet-agent fleet"
STAGES="github-app muse-update probe-trust mac-quarantine linux-linger"

say() { printf '%s\n' "$*"; }
step() { printf '\n  -> %s\n' "$*"; }
header() { printf '\n== Stage %s: %s ==\n' "$1" "$2"; }

ask_yn() { # ask_yn "question" -> 0 for yes; the default is no
  local reply
  printf '%s [y/N] ' "$1"
  read -r reply || return 1
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

pause() { printf '%s' "${1:-Press Enter when done (or Ctrl-C to stop).} "; read -r _ || true; }

open_url() {
  say "  Opening: $1"
  if command -v open >/dev/null 2>&1; then open "$1" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null 2>&1; then xdg-open "$1" >/dev/null 2>&1 || true
  else say "  (open it in your browser)"
  fi
}

rc_file() {
  case "${SHELL:-}" in
    */zsh) printf '%s' "${HOME}/.zshrc" ;;
    */bash) printf '%s' "${HOME}/.bashrc" ;;
    *) printf '%s' "${HOME}/.profile" ;;
  esac
}

# --- status checks: read-only, no network ---------------------------------

check_github_app() { say "manual: done when #26 is closed (the script cannot see the App)"; }

check_muse_update() {
  local rc; rc=$(rc_file)
  if [ "${MUSE_NO_AUTO_UPDATE:-}" = "1" ]; then say "off (MUSE_NO_AUTO_UPDATE=1 in this shell)"
  elif [ -f "$rc" ] && grep -q '^export MUSE_NO_AUTO_UPDATE=1' "$rc"; then say "off (set in $rc; open a new shell)"
  else say "on (Muse updates itself; decide in #28)"
  fi
}

check_probe_trust() {
  if [ -d "$PROBE_DIR" ]; then say "folder exists: $PROBE_DIR (trust is checked by fleet-agent doctor once it exists)"
  else say "folder missing: $PROBE_DIR"
  fi
}

check_mac_quarantine() {
  if [ "$(uname -s)" != "Darwin" ]; then say "not a Mac: nothing to do"; return; fi
  local b p found=0
  for b in $BINARIES; do
    p=$(command -v "$b" 2>/dev/null) || continue
    found=1
    if xattr -p com.apple.quarantine "$p" >/dev/null 2>&1; then say "$b: flagged ($p)"
    else say "$b: clear"
    fi
  done
  [ "$found" = 1 ] || say "no fleet binaries installed yet (plan PR 16)"
}

check_linux_linger() {
  if [ "$(uname -s)" != "Linux" ]; then say "not Linux: nothing to do"; return; fi
  if loginctl show-user "${USER:-$(id -un)}" -p Linger 2>/dev/null | grep -q 'Linger=yes'; then say "on"
  else say "off"
  fi
}

# --- interactive stages ----------------------------------------------------

stage_github_app() {
  header 1 "GitHub App (#26)"
  say "The hub reads issues and PRs through a read-only GitHub App. Only you can create it."
  say "Repository permissions, all Read-only: Metadata, Issues, Pull requests, Checks,"
  say "Commit statuses, Contents. No webhook, no write permission, no account permission."
  say "Later, 'fleetd setup github' (plan PR 13a) writes this for you."
  ask_yn "Open the page that creates a GitHub App now?" || { say "  Skipped."; return; }
  open_url "https://github.com/settings/apps/new"
  step "Name it (for example fleet-board-<you>). Set the permissions above. Turn webhooks off."
  pause
  step "Install the App on the repos fleet-board should watch (the App page, Install App)."
  open_url "https://github.com/settings/apps"
  pause
  if command -v gh >/dev/null 2>&1 && ask_yn "Comment on #26 that the App is created and installed?"; then
    gh issue comment 26 --repo "$REPO" --body "GitHub App created and installed (read-only permissions of ADR 0022). Recorded with scripts/owner-setup.sh." \
      && say "  Commented on #26. Close it when you are ready."
  fi
}

stage_muse_update() {
  header 2 "Muse auto-update (#28)"
  say "Muse updates itself in the background. A new version can change its screens,"
  say "and fleet-board then shows UNKNOWN until its parsers catch up (ADR 0018)."
  say "Current: $(check_muse_update)"
  ask_yn "Turn Muse auto-update off on this machine?" || { say "  Left as it is."; return; }
  local rc; rc=$(rc_file)
  if [ -f "$rc" ] && grep -q '^export MUSE_NO_AUTO_UPDATE=1' "$rc"; then
    say "  Already in $rc."
  elif ask_yn "Add 'export MUSE_NO_AUTO_UPDATE=1' to $rc?"; then
    printf '\n# fleet-board: keep Muse on a known version (#28)\nexport MUSE_NO_AUTO_UPDATE=1\n' >>"$rc"
    say "  Added. Open a new shell for it to take effect."
  fi
}

stage_probe_trust() {
  header 3 "Probe folder trust (C3)"
  say "fleet-agent reads usage in one empty folder: $PROBE_DIR"
  say "Each harness asks once whether to trust a new folder. You answer it, by hand."
  if [ ! -d "$PROBE_DIR" ]; then
    ask_yn "Create $PROBE_DIR now?" || { say "  Skipped."; return; }
    mkdir -p "$PROBE_DIR" && say "  Created."
  fi
  local h any=0
  for h in $HARNESSES; do
    command -v "$h" >/dev/null 2>&1 || continue
    any=1
    step "$h: in another terminal, run:  cd \"$PROBE_DIR\" && $h"
    say "     Answer the trust question with yes, then quit $h. Leave the folder empty."
    pause
  done
  [ "$any" = 1 ] || say "  No harness found on this machine."
}

stage_mac_quarantine() {
  header 4 "macOS quarantine flag (after install)"
  if [ "$(uname -s)" != "Darwin" ]; then say "  Not a Mac. Nothing to do."; return; fi
  local b p any=0
  for b in $BINARIES; do
    p=$(command -v "$b" 2>/dev/null) || continue
    xattr -p com.apple.quarantine "$p" >/dev/null 2>&1 || continue
    any=1
    if ask_yn "Clear the quarantine flag on $p?"; then
      xattr -d com.apple.quarantine "$p" && say "  Cleared."
    fi
  done
  [ "$any" = 1 ] || say "  Nothing flagged (or nothing installed yet)."
}

stage_linux_linger() {
  header 5 "Linux linger"
  if [ "$(uname -s)" != "Linux" ]; then say "  Not Linux. Nothing to do."; return; fi
  if [ "$(check_linux_linger)" = "on" ]; then say "  Already on."; return; fi
  say "Without linger, systemd stops fleet-agent when you log out."
  if ask_yn "Run 'sudo loginctl enable-linger ${USER:-$(id -un)}' now? (asks for your password)"; then
    sudo loginctl enable-linger "${USER:-$(id -un)}" && say "  On."
  fi
}

# --- dispatch -------------------------------------------------------------

nth_stage() { # nth_stage N -> stage name
  local i=1 s
  for s in $STAGES; do [ "$i" = "$1" ] && { printf '%s' "$s"; return 0; }; i=$((i + 1)); done
  return 1
}

run_stage() {
  case "$1" in
    github-app) stage_github_app ;;
    muse-update) stage_muse_update ;;
    probe-trust) stage_probe_trust ;;
    mac-quarantine) stage_mac_quarantine ;;
    linux-linger) stage_linux_linger ;;
  esac
}

check_stage() {
  case "$1" in
    github-app) check_github_app ;;
    muse-update) check_muse_update ;;
    probe-trust) check_probe_trust ;;
    mac-quarantine) check_mac_quarantine ;;
    linux-linger) check_linux_linger ;;
  esac
}

main() {
  local mode="${1:-}" i s
  case "$mode" in
    --list)
      i=1; for s in $STAGES; do say "$i $s"; i=$((i + 1)); done ;;
    --check)
      i=1; for s in $STAGES; do printf '%s %-15s ' "$i" "$s"; check_stage "$s"; i=$((i + 1)); done ;;
    --stage)
      s=$(nth_stage "${2:-}") || { say "unknown stage: ${2:-} (see --list)" >&2; exit 2; }
      [ -t 0 ] || { say "needs a terminal" >&2; exit 2; }
      run_stage "$s" ;;
    "")
      [ -t 0 ] || { say "needs a terminal; use --check or --list" >&2; exit 2; }
      say "fleet-board owner setup. Each stage asks before it changes anything."
      for s in $STAGES; do run_stage "$s"; done
      say ""; say "Where things stand:"; main --check ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//' ;;
    *) say "unknown option: $mode (see --help)" >&2; exit 2 ;;
  esac
}

main "$@"
