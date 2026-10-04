#!/usr/bin/env bash
#
# test-changed-go-packages.sh: seeded cases for scripts/changed-go-packages.sh.
#
# Builds a throwaway git repo (never this one) with a tiny Go module:
#   a (no imports), b (imports a), c (independent), d (its test imports a)
# and checks which packages the script names for each kind of change. Runs on
# the macOS system bash 3.2.
#
# Usage: scripts/test-changed-go-packages.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/scripts/changed-go-packages.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
repo="$work/repo"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

# Keep git from reading the caller's repo or config.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com

g() { git -C "$repo" "$@"; }

# fresh: a clean repo where origin/main is the base and a work branch follows.
fresh() {
  rm -rf "$repo"
  mkdir -p "$repo/a" "$repo/b" "$repo/c" "$repo/d" "$repo/docs" "$repo/api"
  g init -q -b main
  printf 'module example.com/m\n\ngo 1.21\n' > "$repo/go.mod"
  printf 'package a\n\nfunc A() int { return 1 }\n' > "$repo/a/a.go"
  printf 'package b\n\nimport "example.com/m/a"\n\nfunc B() int { return a.A() }\n' > "$repo/b/b.go"
  printf 'package c\n\nfunc C() int { return 3 }\n' > "$repo/c/c.go"
  printf 'package d\n\nfunc D() int { return 4 }\n' > "$repo/d/d.go"
  printf 'package d\n\nimport (\n\t"testing"\n\n\t"example.com/m/a"\n)\n\nfunc TestD(t *testing.T) { _ = a.A() }\n' > "$repo/d/d_test.go"
  printf 'notes\n' > "$repo/docs/x.md"
  printf 'syntax = "proto3";\n' > "$repo/api/x.proto"
  printf 'all:\n' > "$repo/Makefile"
  g add -A
  g commit -q -m base
  g update-ref refs/remotes/origin/main HEAD
  g checkout -q -b work
}

# expect <name> <expected output, space separated> [base ref]
expect() {
  local name="$1" want="$2" out
  out="$(cd "$repo" && FLEET_CHANGED_PKGS_REPO_ROOT="$repo" "$script" ${3:+"$3"} 2>"$work/err" | tr '\n' ' ' | sed 's/ $//')"
  if [ "$out" = "$want" ]; then
    ok
  else
    bad "$name: got '$out', want '$want' ($(cat "$work/err"))"
  fi
}

fresh
expect "nothing changed names nothing" ""

fresh
printf 'package c\n\nfunc C() int { return 33 }\n' > "$repo/c/c.go"
g commit -q -am "edit c"
expect "a committed change names its package" "./c"

fresh
printf 'package a\n\nfunc A() int { return 11 }\n' > "$repo/a/a.go"
g commit -q -am "edit a"
expect "importers and test importers are named too" "./a ./b ./d"

fresh
printf 'package c\n\nfunc C() int { return 34 }\n' > "$repo/c/c.go"
expect "an unstaged edit counts" "./c"

fresh
printf 'package c\n\nfunc C2() int { return 5 }\n' > "$repo/c/c2.go"
expect "an untracked new file counts" "./c"

fresh
mkdir "$repo/c/testdata"
printf 'x\n' > "$repo/c/testdata/f.txt"
g add -A
g commit -q -m "fixture"
expect "a fixture file names its package" "./c"

fresh
mkdir "$repo/c/testdata"
printf 'package fixture\n' > "$repo/c/testdata/f.go"
g add -A
g commit -q -m "Go fixture"
expect "a Go fixture under testdata names the owning package" "./c"

fresh
mkdir -p "$repo/c/testdata/deep/er"
printf 'package er\n' > "$repo/c/testdata/deep/er/f.go"
g add -A
g commit -q -m "nested Go fixture"
expect "a Go fixture nested under testdata names the owning package" "./c"

fresh
mkdir -p "$repo/testdata/only"
printf 'package only\n' > "$repo/testdata/only/f.go"
g add -A
g commit -q -m "fixture with no owning package"
expect "a Go fixture with no owning package names nothing" ""

# A module that has a package at its root: the base commit carries root.go.
rootfresh() {
  fresh
  g checkout -q main
  printf 'package m\n\nfunc Root() int { return 0 }\n' > "$repo/root.go"
  g add -A
  g commit -q -m "base gains root.go"
  g update-ref refs/remotes/origin/main HEAD
  g checkout -q work
  g rebase -q main
}

rootfresh
printf 'package m\n\nfunc Root() int { return 1 }\n' > "$repo/root.go"
g commit -q -am "edit root.go"
expect "a .go file at the module root names the root package" "."

rootfresh
printf 'more\n' > "$repo/docs/x.txt"
g add -A
g commit -q -m "a non-Go file elsewhere"
expect "a non-Go file elsewhere does not name the root package" ""

fresh
printf 'readme\n' > "$repo/c/README.md"
g add -A
g commit -q -m "readme beside Go files"
expect "a markdown file beside Go files adds nothing" ""

fresh
printf 'more\n' >> "$repo/docs/x.md"
g commit -q -am "docs"
expect "a docs-only change names nothing" ""

fresh
g rm -q b/b.go
g commit -q -m "delete b"
expect "a deleted package names nothing" ""

fresh
g checkout -q main
printf 'package a\n\nfunc Old() int { return 2 }\n' > "$repo/a/old.go"
g add -A
g commit -q -m "base gains a/old.go"
g update-ref refs/remotes/origin/main HEAD
g checkout -q work
g rebase -q main
g rm -q a/old.go
g commit -q -m "delete a/old.go"
expect "a deleted file in a live package names the package" "./a ./b ./d"

fresh
printf 'module example.com/m\n\ngo 1.22\n' > "$repo/go.mod"
g commit -q -am "go.mod"
expect "go.mod change means every package" "./..."

fresh
printf 'x\n' > "$repo/Makefile"
g commit -q -am "Makefile"
expect "Makefile change means every package" "./..."

fresh
printf 'syntax = "proto3";\n// c\n' > "$repo/api/x.proto"
g commit -q -am "proto"
expect "a contract change means every package" "./..."

fresh
printf 'X=1\n' > "$repo/tool-versions.env"
g add -A
g commit -q -m "versions"
expect "the versions file means every package" "./..."

fresh
printf 'package c\n\nfunc C() int { return 35 }\n' > "$repo/c/c.go"
g commit -q -am "edit c"
expect "a missing base ref means every package" "./..." "refs/remotes/origin/nope"

fresh
printf 'package c\n\nfunc C() int { return 36 }\n' > "$repo/c/c.go"
g commit -q -am "edit c"
g checkout -q main
printf 'package b\n\nimport "example.com/m/a"\n\nfunc B() int { return a.A() + 1 }\n' > "$repo/b/b.go"
g commit -q -am "main moves on (b)"
g update-ref refs/remotes/origin/main HEAD
g checkout -q work
expect "a base that moved on is compared at the merge base" "./c"

printf 'test-changed-go-packages: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
