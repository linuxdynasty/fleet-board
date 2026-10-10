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

  # Stage 2 through a pseudo-terminal: every change asks first, and "yes" twice
  # appends exactly three lines.
  printf 'keep\n' >"$home/.zshrc"
  drive "$sh" "$home" 'n\n'
  [ "$(cat "$home/.zshrc")" = "keep" ] && ok || bad "$sh" "answer no leaves .zshrc unchanged"
  drive "$sh" "$home" 'y\nn\n'
  [ "$(cat "$home/.zshrc")" = "keep" ] && ok || bad "$sh" "yes then no leaves .zshrc unchanged"
  drive "$sh" "$home" 'y\ny\n'
  [ "$(wc -l <"$home/.zshrc" | tr -d ' ')" = 4 ] && ok || bad "$sh" "yes twice appends three lines"
  tail -n 1 "$home/.zshrc" | grep -qx 'export MUSE_NO_AUTO_UPDATE=1' && ok || bad "$sh" "the appended line is the export"
  drive "$sh" "$home" 'y\ny\n'
  [ "$(wc -l <"$home/.zshrc" | tr -d ' ')" = 4 ] && ok || bad "$sh" "a second run appends nothing"

  # An unsupported shell gets the line printed and no file written.
  rm -f "$home/.zshrc"
  drive "$sh" "$home" 'y\ny\n' /usr/bin/fish
  [ -z "$(ls -A "$home")" ] && ok || bad "$sh" "unsupported shell writes nothing"
  # bash on macOS writes ~/.bash_profile only when it exists.
  if [ "$(uname -s)" = Darwin ]; then
    printf 'keep\n' >"$home/.bash_profile"
    drive "$sh" "$home" 'y\ny\n' /bin/bash
    tail -n 1 "$home/.bash_profile" | grep -qx 'export MUSE_NO_AUTO_UPDATE=1' && ok || bad "$sh" "bash on macOS uses .bash_profile"
    [ ! -e "$home/.bashrc" ] && ok || bad "$sh" "bash on macOS leaves .bashrc alone"
  fi

  # Stage 3 prints the steps of the 01 C3 table for the harnesses it finds.
  bin=$(mktemp -d)
  for h in claude codex muse cursor-agent opencode; do printf '#!/bin/sh\n' >"$bin/$h"; chmod +x "$bin/$h"; done
  log="$home.out"
  DRIVE_LOG="$log" drive "$sh" "$home" '\n\n\n\n\n\n' /bin/zsh 3 "$bin"
  out=$(cat "$log"); rm -f "$log"
  [ -n "$out" ] && ok || bad "$sh" "stage 3 printed something"
  printf '%s\n' "$out" | grep -qi 'provisional' && bad "$sh" "stage 3 no longer says provisional" || ok
  printf '%s\n' "$out" | grep -q 'Down' && ok || bad "$sh" "stage 3 tells Claude Code to press Down first"
  printf '%s\n' "$out" | grep -q 'Trust this workspace' && ok || bad "$sh" "stage 3 names the Cursor screen"
  printf '%s\n' "$out" | grep -q 'Do you trust this workspace' && ok || bad "$sh" "stage 3 names the Muse screen"
  printf '%s\n' "$out" | grep -q 'git repo' && ok || bad "$sh" "stage 3 says Codex asks only in a git repo"
  printf '%s\n' "$out" | grep -q 'opencode: no trust' && ok || bad "$sh" "stage 3 says OpenCode has no trust screen"
  printf '%s\n' "$out" | grep -qi 'with yes' && bad "$sh" "stage 3 has no blanket yes" || ok
  rm -rf "$bin"

  rm -rf "$home"
}

drive() { # drive SHELL HOME ANSWERS [LOGIN_SHELL] [STAGE] [PATH_DIR]: run a stage on a pseudo-terminal
  python3 - "$1" "$script" "$2" "$3" "${4:-/bin/zsh}" "${5:-2}" "${6:-}" <<'PY'
import os, subprocess, sys
sh, script, home, answers, login_shell, stage, extra = sys.argv[1:8]
master, slave = os.openpty()
env = {"HOME": home, "SHELL": login_shell, "PATH": (extra + ":" if extra else "") + os.environ["PATH"], "MUSE_NO_AUTO_UPDATE": ""}
p = subprocess.Popen([sh, script, "--stage", stage], stdin=slave, stdout=slave, stderr=slave, env=env)
os.close(slave)
os.write(master, answers.encode().decode("unicode_escape").encode())
out = b""
while True:
    try:
        chunk = os.read(master, 4096)
        if not chunk:
            break
        out += chunk
    except OSError:
        break
p.wait(timeout=20)
if os.environ.get("DRIVE_LOG"):
    open(os.environ["DRIVE_LOG"], "wb").write(out)
PY
}

for sh in /bin/bash /opt/homebrew/bin/bash /usr/bin/bash; do
  [ -x "$sh" ] || continue
  run_suite "$sh"
done

printf 'test-owner-setup: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" = 0 ]
