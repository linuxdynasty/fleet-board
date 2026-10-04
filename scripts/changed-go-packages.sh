#!/usr/bin/env bash
#
# changed-go-packages.sh: print the Go packages a change affects, one per line
# as ./path, for `make test` and `make lint` to run on.
#
# Usage: scripts/changed-go-packages.sh [BASE_REF]
#   BASE_REF defaults to $FLEET_BASE_REF, then origin/main.
#
# What counts as changed: every file that differs from the merge base of
# BASE_REF and HEAD (committed, staged, unstaged) plus untracked files, so the
# same command works for a developer's working tree and for a CI checkout.
# A changed file belongs to the nearest directory at or above it that holds
# .go files (never one under testdata/, which go ignores: a fixture belongs to
# the package above it); markdown files never name a package. Packages that
# import a changed package, directly or through other packages (tests
# included), are named too.
#
# Prints ./... (every package) instead when the build inputs change: go.mod,
# go.sum, the Makefile, tool-versions.env, .golangci.yml, buf.yaml,
# buf.gen.yaml, anything under api/ (they regenerate gen/), or this script.
# It also prints ./... when BASE_REF or a merge base cannot be found, because
# a guess would hide a change. Prints nothing when no Go package is affected.
#
# Needs `go list` to work, so run `make gen` first in a fresh checkout.
# Portable to the macOS system bash 3.2 and BSD awk. FLEET_CHANGED_PKGS_REPO_ROOT
# points the script at another tree (used to test it against seeded changes).
set -euo pipefail

root="${FLEET_CHANGED_PKGS_REPO_ROOT:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$root"

base="${1:-${FLEET_BASE_REF:-origin/main}}"

everything() {
  printf 'changed-go-packages: %s; using every package.\n' "$1" >&2
  printf './...\n'
  exit 0
}

git rev-parse --verify -q "$base^{commit}" > /dev/null || everything "base ref $base not found"
merge_base="$(git merge-base "$base" HEAD)" || everything "no merge base with $base"

files="$({
  git diff --no-renames --name-only "$merge_base"
  git ls-files --others --exclude-standard
} | sort -u)"

dirs=""
while IFS= read -r f; do
  [ -n "$f" ] || continue
  case "$f" in
    go.mod | go.sum | Makefile | tool-versions.env | .golangci.yml | buf.yaml | buf.gen.yaml | api/* | scripts/changed-go-packages.sh)
      everything "$f changed"
      ;;
    *.md) continue ;;
  esac
  # The owning package is the nearest directory at or above the file that holds
  # .go files. A directory at or under testdata/ is never a package (go ignores
  # it), so a fixture belongs to the package above. Only a file directly in the
  # module root can name the root package; a file deeper down stops below it.
  d="$(dirname "$f")"
  while :; do
    case "/$d/" in
      */testdata/*) ;;
      *)
        if ls "$d"/*.go > /dev/null 2>&1; then
          dirs="$dirs
$d"
          break
        fi
        ;;
    esac
    [ "$d" = "." ] && break # a file in the root: the root was just checked
    [ "$d" = "/" ] && break
    d="$(dirname "$d")"
    [ "$d" = "." ] && break # a deeper file never names the root package
  done
done <<EOF
$files
EOF

dirs="$(printf '%s\n' "$dirs" | sed '/^$/d' | sort -u)"
[ -n "$dirs" ] || exit 0

module="$(go list -m)"
imports="$(go list -f '{{.ImportPath}}|{{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./...)"

# Grow the changed set with every package that imports a member, until stable.
# The import list goes in through the environment: BSD awk rejects a newline in -v.
printf '%s\n' "$dirs" | MODULE="$module" IMPORTS="$imports" awk '
  BEGIN {
    module = ENVIRON["MODULE"]
    n = split(ENVIRON["IMPORTS"], lines, "\n")
    for (i = 1; i <= n; i++) {
      split(lines[i], parts, "|")
      pkgs[i] = parts[1]
      deps[i] = parts[2]
    }
  }
  { set[($0 == ".") ? module : module "/" $0] = 1 }
  END {
    changed = 1
    while (changed) {
      changed = 0
      for (i = 1; i <= n; i++) {
        if (pkgs[i] in set) continue
        m = split(deps[i], d, " ")
        for (j = 1; j <= m; j++) {
          if (d[j] in set) { set[pkgs[i]] = 1; changed = 1; break }
        }
      }
    }
    for (p in set) {
      # A changed directory that go list does not know (a deleted package) is skipped.
      known = 0
      for (i = 1; i <= n; i++) if (pkgs[i] == p) known = 1
      if (!known) continue
      print "." substr(p, length(module) + 1)
    }
  }
' | sort
