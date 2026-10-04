#!/usr/bin/env bash
#
# smoke-binaries.sh: build every cmd/<name> binary and run `<name> --version`
# once. Each must build, exit 0 and print a line that starts with its own name
# (fleetd prints "fleetd dev"). It is the cheapest proof that the binaries are
# wired and linked, and `make gates` and CI run it after `go build ./...`.
#
# Usage: scripts/smoke-binaries.sh
# FLEET_SMOKE_REPO_ROOT points the script at another module (used to test it
# against seeded violations). Portable to the macOS system bash 3.2.
set -euo pipefail

root="${FLEET_SMOKE_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$root"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

count=0
problems=""
for dir in cmd/*/; do
  [ -d "$dir" ] || continue
  name="$(basename "$dir")"
  count=$((count + 1))
  if ! build_out="$(CGO_ENABLED=0 go build -o "$work/$name" "./cmd/$name" 2>&1)"; then
    problems="$problems
  $name: does not build: $build_out"
    continue
  fi
  if ! out="$("$work/$name" --version 2>&1)"; then
    problems="$problems
  $name: --version exited non-zero: $out"
    continue
  fi
  case "$out" in
    "$name "*) ;;
    *) problems="$problems
  $name: --version printed '$out', want a line starting with '$name '" ;;
  esac
done

if [ "$count" -eq 0 ]; then
  printf 'smoke-binaries: no binaries found under cmd/\n' >&2
  exit 1
fi
if [ -n "$problems" ]; then
  printf 'smoke-binaries: failures:%s\n' "$problems" >&2
  exit 1
fi
printf 'smoke-binaries: %s binaries build and print their name with --version.\n' "$count"
