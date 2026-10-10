#!/usr/bin/env bash
#
# check-generated-headers.sh: fail if a Go file outside gen/ carries a
# generated-code header (`// Code generated ... DO NOT EDIT.` before its
# package clause).
#
# .golangci.yml has `exclusions.generated: strict`, so golangci-lint skips any
# file with that header. All generated code lives in gen/ (never committed); a
# hand-written file with the header would escape every rule, including the
# os/exec import rule of 01 principle 8. Tracked and untracked files are both
# read; gen/ and testdata/ are skipped (Go ignores testdata).
#
# Portable to the macOS system bash 3.2. FLEET_GENERATED_HEADERS_REPO_ROOT points
# the script at another tree (used to test it against seeded files).
set -euo pipefail

root="${FLEET_GENERATED_HEADERS_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$root"

bad=""
checked=0
while IFS= read -r -d '' f; do
  case "$f" in
    gen/* | */gen/* | testdata/* | */testdata/*) continue ;;
  esac
  [ -f "$f" ] && [ ! -L "$f" ] || continue
  checked=$((checked + 1))
  # The header counts anywhere before the package clause (golang.org/s/generatedcode).
  if awk '/^package[ \t]/ { exit 1 } /^\/\/ Code generated .* DO NOT EDIT\.$/ { found = 1; exit 0 } END { exit found ? 0 : 1 }' "$f"; then
    bad="$bad $f"
  fi
done < <(git ls-files -z --cached --others --exclude-standard -- '*.go')

if [ -n "$bad" ]; then
  printf 'check-generated-headers: generated-code header in a file outside gen/ (the linter would skip it):%s\n' "$bad" >&2
  exit 1
fi
printf 'check-generated-headers: %d Go files, none outside gen/ with a generated-code header.\n' "$checked"
