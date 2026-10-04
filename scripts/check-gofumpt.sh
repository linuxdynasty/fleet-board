#!/usr/bin/env bash
#
# check-gofumpt.sh: fail if a Go file is not gofumpt-formatted.
#
# Checks every tracked or untracked-but-not-ignored .go file, so generated code
# under gen/ (gitignored) is skipped. `main` is always formatted, so this is
# the same set as "the changed files" and needs no base ref.
#
# Usage: scripts/check-gofumpt.sh
# The binary is $FLEET_GOFUMPT, else .bin/gofumpt (built by `make tools` at the
# version in tool-versions.env). FLEET_GOFUMPT_REPO_ROOT points the script at
# another tree (used to test it against a seeded violation).
# Portable to the macOS system bash 3.2.
set -euo pipefail

root="${FLEET_GOFUMPT_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi
bin="${FLEET_GOFUMPT:-}"
if [ -z "$bin" ]; then
  bin="$(cd "$(dirname "$0")/.." && pwd)/.bin/gofumpt"
fi
if [ ! -x "$bin" ]; then
  printf 'check-gofumpt: %s is missing; run `make tools`\n' "$bin" >&2
  exit 1
fi

cd "$root"
unformatted=""
checked=0
while IFS= read -r -d '' f; do
  [ -f "$f" ] || continue # tracked but deleted in the working tree
  checked=$((checked + 1))
  out="$("$bin" -l "$f")"
  if [ -n "$out" ]; then
    unformatted="$unformatted $f"
  fi
done < <(git ls-files -z --cached --others --exclude-standard -- '*.go')

if [ -n "$unformatted" ]; then
  printf 'check-gofumpt: not formatted:%s\nFix with: .bin/gofumpt -l -w <files>\n' "$unformatted" >&2
  exit 1
fi
printf 'check-gofumpt: %s Go files are formatted.\n' "$checked"
