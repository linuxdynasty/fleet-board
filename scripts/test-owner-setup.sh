#!/usr/bin/env bash
# Smoke tests for scripts/owner-setup.sh, run with each bash on the machine
# (the macOS system bash 3.2 and a newer one if present). Uses a throwaway HOME,
# so it never touches the real shell profile or probe folder.
set -u
here=$(cd "$(dirname "$0")" && pwd)
script="$here/owner-setup.sh"
pass=0 fail=0

ok() { pass=$((pass + 1)); }
bad() { fail=$((fail + 1)); printf 'FAIL [%s]: %s\n' "$1" "$2"; }

run_suite() {
  local sh="$1" home out rc
  home=$(mktemp -d)

  "$sh" -n "$script" && ok || bad "$sh" "syntax"

  out=$(HOME="$home" "$sh" "$script" --list)
  [ "$(printf '%s\n' "$out" | wc -l | tr -d ' ')" = 5 ] && ok || bad "$sh" "--list prints 5 stages"

  out=$(HOME="$home" SHELL=/bin/zsh MUSE_NO_AUTO_UPDATE= "$sh" "$script" --check); rc=$?
  [ "$rc" = 0 ] && ok || bad "$sh" "--check exits 0 (got $rc)"
  [ -z "$(ls -A "$home")" ] && ok || bad "$sh" "--check changes nothing in HOME"
  printf '%s\n' "$out" | grep -q 'muse-update *on' && ok || bad "$sh" "--check reports Muse auto-update on"

  printf 'export MUSE_NO_AUTO_UPDATE=1\n' >"$home/.zshrc"
  out=$(HOME="$home" SHELL=/bin/zsh MUSE_NO_AUTO_UPDATE= "$sh" "$script" --check)
  printf '%s\n' "$out" | grep -q 'muse-update *off' && ok || bad "$sh" "--check sees the profile line"

  HOME="$home" "$sh" "$script" </dev/null >/dev/null 2>&1; rc=$?
  [ "$rc" = 2 ] && ok || bad "$sh" "interactive run without a terminal exits 2 (got $rc)"

  HOME="$home" "$sh" "$script" --stage 9 </dev/null >/dev/null 2>&1; rc=$?
  [ "$rc" = 2 ] && ok || bad "$sh" "unknown stage exits 2 (got $rc)"

  rm -rf "$home"
}

for sh in /bin/bash /opt/homebrew/bin/bash /usr/bin/bash; do
  [ -x "$sh" ] || continue
  run_suite "$sh"
done

printf 'test-owner-setup: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ]
