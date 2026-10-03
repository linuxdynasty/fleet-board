#!/usr/bin/env bash
#
# check-file-cap.sh: fail if a tracked text file has 500 or more lines
# (AGENTS.md: files stay under 500 lines, source and docs alike).
#
# Skipped: symlinks (the skill links point at directories), binary files, and
# files that are generated or are lock files by nature: go.sum,
# package-lock.json, anything under gen/ or testdata/.
#
# Portable to the macOS system bash 3.2. FLEET_FILE_CAP_REPO_ROOT points the
# script at another tree (used to test it against a seeded violation).
set -euo pipefail

root="${FLEET_FILE_CAP_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi
cap=500
cd "$root"

over=""
checked=0
while IFS= read -r -d '' f; do
  [ -L "$f" ] && continue
  [ -f "$f" ] || continue
  case "$f" in
    go.sum | package-lock.json | */go.sum | */package-lock.json | gen/* | */gen/* | testdata/* | */testdata/*) continue ;;
  esac
  grep -Iq . "$f" 2>/dev/null || continue # binary or empty
  checked=$((checked + 1))
  n="$(wc -l < "$f" | tr -d ' ')"
  if [ "$n" -ge "$cap" ]; then
    over="$over $f($n)"
  fi
done < <(git ls-files -z)

if [ -n "$over" ]; then
  printf 'check-file-cap: files at or over the %s-line cap:%s\n' "$cap" "$over" >&2
  exit 1
fi
printf 'check-file-cap: %s tracked text files are under %s lines.\n' "$checked" "$cap"
