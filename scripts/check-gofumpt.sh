#!/usr/bin/env bash
#
# check-gofumpt.sh: fail if a Go file is not gofumpt-formatted; with --write,
# format them instead (this is what `make fmt` runs).
#
# Both modes read every tracked or untracked-but-not-ignored .go file, so
# generated code under gen/ (gitignored) is skipped, and the check and the fix
# can never disagree about which files they cover. `main` is always formatted,
# so this is the same set as "the changed files" and needs no base ref.
#
# The check fails, loudly, when git cannot list the files or when there are no
# Go files at all: a gate that sees nothing must not pass.
#
# Usage: scripts/check-gofumpt.sh [--write]
# The binary is $FLEET_GOFUMPT, else .bin/gofumpt (built by `make tools` at the
# version in tool-versions.env). FLEET_GOFUMPT_REPO_ROOT points the script at
# another tree (used to test it against a seeded violation).
# Portable to the macOS system bash 3.2.
set -euo pipefail

write=0
case "${1:-}" in
  "") ;;
  --write) write=1 ;;
  *)
    printf 'usage: check-gofumpt.sh [--write]\n' >&2
    exit 2
    ;;
esac

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
# A temp file, not a process substitution: its exit status would be lost.
list="$(mktemp)"
trap 'rm -f "$list"' EXIT
if ! git ls-files -z --cached --others --exclude-standard -- '*.go' > "$list"; then
  printf 'check-gofumpt: could not list the Go files with git in %s\n' "$root" >&2
  exit 1
fi

changed=""
checked=0
while IFS= read -r -d '' f; do
  [ -f "$f" ] || continue # tracked but deleted in the working tree
  checked=$((checked + 1))
  if [ "$write" -eq 1 ]; then
    out="$("$bin" -l -w "$f")"
  else
    out="$("$bin" -l "$f")"
  fi
  if [ -n "$out" ]; then
    changed="$changed $f"
  fi
done < "$list"

if [ "$checked" -eq 0 ]; then
  printf 'check-gofumpt: no Go files found in %s; refusing to pass on an empty list\n' "$root" >&2
  exit 1
fi

if [ "$write" -eq 1 ]; then
  if [ -n "$changed" ]; then
    printf 'check-gofumpt: formatted:%s\n' "$changed"
  fi
  printf 'check-gofumpt: %s Go files are formatted.\n' "$checked"
  exit 0
fi
if [ -n "$changed" ]; then
  printf 'check-gofumpt: not formatted:%s\nFix with: make fmt\n' "$changed" >&2
  exit 1
fi
printf 'check-gofumpt: %s Go files are formatted.\n' "$checked"
