#!/usr/bin/env bash
#
# test-check-pr-base.sh: seeded RED/GREEN cases for scripts/check-pr-base.sh,
# the pr-meta check that a pull request targets main (#107).
#
# The base branch name is author-controlled text, so it reaches the script
# only through the PR_BASE_REF env var and must be treated as data: a name with
# a `;`, `$(...)` or backticks fails as a wrong base and runs nothing. Every
# case runs in a throwaway directory, so a marker file left behind means the
# script executed part of the name.
# Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-check-pr-base.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/scripts/check-pr-base.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
  [ -n "${out:-}" ] && printf '  output: %s\n' "$out" >&2
}

# check <name> <base> <want rc> <substring of the output, or empty>: runs the
# script in a fresh empty directory and fails the case if any file appears there.
check() {
  local name="$1" base="$2" want="$3" sub="$4" dir left
  dir="$work/case"
  rm -rf "$dir"
  mkdir -p "$dir"
  out="$(cd "$dir" && PR_BASE_REF="$base" "$script" 2>&1)"
  rc=$?
  left="$(ls -A "$dir")"
  if [ -n "$left" ]; then
    bad "$name: the script left files behind (it ran part of the name): $left"
  elif [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want"
  elif [ -n "$sub" ] && ! printf '%s' "$out" | grep -q -F -- "$sub"; then
    bad "$name: output lacks '$sub'"
  else
    ok
  fi
}

# RED: a base other than main fails and says the PR must target main.
check "base feat/x" "feat/x" 1 "main"

# The message tells the author what works: retarget, then push again. A rerun of
# the job reuses the original event data (github.base_ref included), so it
# would fail the same way; the message says so.
check "message: retarget to main" "feat/x" 1 "Retarget the pull request to main"
check "message: then push again" "feat/x" 1 "then push again"
check "message: a rerun reuses the old base" "feat/x" 1 "a rerun reuses the old base"

# RED: a hostile name is just a wrong base; nothing runs.
check "base 'main; touch pwned'" "main; touch pwned" 1 "main"
check "message for a hostile base" "main; touch pwned" 1 "then push again"
check "base 'main\$(touch pwned)'" 'main$(touch pwned)' 1 "main"
check "base with backticks" 'main`touch pwned`' 1 "main"

# RED: names that only look like main are not main.
check "base 'main ' (trailing space)" "main " 1 "main"
check "base refs/heads/main" "refs/heads/main" 1 "main"
check "base master" "master" 1 "main"

# RED: no base at all (the env var empty or missing) fails.
check "empty base" "" 1 "main"
dir="$work/case"
rm -rf "$dir"
mkdir -p "$dir"
out="$(cd "$dir" && env -u PR_BASE_REF "$script" 2>&1)"
rc=$?
if [ "$rc" -eq 1 ]; then ok; else bad "unset PR_BASE_REF: exit $rc, want 1"; fi

# GREEN: main passes.
check "base main" "main" 0 ""

printf 'test-check-pr-base: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
