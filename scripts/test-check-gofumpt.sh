#!/usr/bin/env bash
#
# test-check-gofumpt.sh: seeded RED/GREEN cases for scripts/check-gofumpt.sh.
#
# Uses a throwaway git repo (never this one) and the pinned gofumpt from
# `make tools` (.bin/gofumpt, or $FLEET_GOFUMPT). Runs on the macOS system
# bash 3.2.
#
# Usage: scripts/test-check-gofumpt.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
check="$root/scripts/check-gofumpt.sh"
bin="${FLEET_GOFUMPT:-$root/.bin/gofumpt}"
if [ ! -x "$bin" ]; then
  printf 'test-check-gofumpt: %s is missing; run `make tools` first\n' "$bin" >&2
  exit 1
fi
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

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

rm -rf "$repo"
mkdir -p "$repo/p" "$repo/gen/go"
git -C "$repo" init -q
printf 'package p\n\nfunc F() int { return 1 }\n' > "$repo/p/ok.go"

# run <name> <want rc> <substring of the output, or empty>
run() {
  local name="$1" want="$2" sub="$3" out rc
  out="$(FLEET_GOFUMPT_REPO_ROOT="$repo" FLEET_GOFUMPT="$bin" "$check" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want ($out)"
    return
  fi
  if [ -n "$sub" ]; then
    case "$out" in
      *"$sub"*) ;;
      *)
        bad "$name: output lacks '$sub' ($out)"
        return
        ;;
    esac
  fi
  ok
}

run "formatted file passes" 0 ""

# gofumpt wants "var ( ... )" blocks without a leading blank line and a
# grouped const; a plain unformatted body is enough: wrong indentation.
printf 'package p\n\nfunc G() int {\nreturn 2\n}\n' > "$repo/p/bad.go"
run "untracked unformatted file fails and is named" 1 "p/bad.go"

git -C "$repo" add p/bad.go
run "staged unformatted file fails" 1 "p/bad.go"

printf 'package p\n\nfunc G() int {\n\treturn 2\n}\n' > "$repo/p/bad.go"
run "formatted after the fix passes" 0 ""

printf 'package p\n\nfunc H() int {\nreturn 3\n}\n' > "$repo/gen/go/x.go"
printf 'gen/\n' > "$repo/.gitignore"
run "gitignored generated code is not checked" 0 ""

# gofumpt is stricter than gofmt: a leading blank line in a block is accepted
# by gofmt and rewritten by gofumpt.
printf 'package p\n\nfunc K() int {\n\n\treturn 4\n}\n' > "$repo/p/strict.go"
run "a gofumpt-only difference fails" 1 "p/strict.go"

printf 'test-check-gofumpt: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
