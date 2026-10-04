#!/usr/bin/env bash
#
# test-make-base-ref.sh: BASE_REF is data, never shell. A BASE_REF with a
# space or a `;` must not run anything: the Makefile passes "$BASE_REF" quoted
# through the environment (it is exported), not pasted into the recipe text.
#
# Two kinds of case, both seeded:
#   - `make -n` prints each recipe; the raw hostile value must appear nowhere
#     in it (test, lint, diff-check and gates all read BASE_REF);
#   - `make diff-check` really runs with a value that would create a marker file
#     if it were executed; the marker must not exist.
# Runs on the macOS system bash 3.2. Needs make and git.
#
# Usage: scripts/test-make-base-ref.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

marker="$work/marker"
hostile="x; touch $marker"
spaced="no such ref"

# -n cases: the recipe text must not contain the raw value.
for target in test lint diff-check gates; do
  out="$(cd "$root" && make -n "$target" BASE_REF="$hostile" 2>&1)"
  case "$out" in
    *"touch $marker"*) bad "make -n $target pastes BASE_REF into the recipe text" ;;
    *) ok ;;
  esac
done

# Real run: the hostile value is just an unknown ref, so nothing may execute.
rm -f "$marker"
out="$(cd "$root" && make diff-check BASE_REF="$hostile" 2>&1)"
rc=$?
if [ -e "$marker" ]; then
  bad "make diff-check ran the code inside BASE_REF"
elif [ "$rc" -ne 0 ]; then
  bad "make diff-check with an unknown ref should still run git diff --check: exit $rc ($out)"
else
  ok
fi

out="$(cd "$root" && make diff-check BASE_REF="$spaced" 2>&1)"
rc=$?
if [ "$rc" -ne 0 ]; then
  bad "make diff-check with a spaced ref: exit $rc ($out)"
else
  ok
fi

printf 'test-make-base-ref: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
