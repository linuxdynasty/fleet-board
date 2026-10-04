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

# run_args <name> <want rc> <substring> <args...>: like run, with arguments.
run_args() {
  local name="$1" want="$2" sub="$3" out rc
  shift 3
  out="$(FLEET_GOFUMPT_REPO_ROOT="$repo" FLEET_GOFUMPT="$bin" "$check" "$@" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: exit $rc, want $want ($out)"
    return
  fi
  case "$out" in
    *"$sub"*) ok ;;
    *) bad "$name: output lacks '$sub' ($out)" ;;
  esac
}

# --write formats the same file set the check reads (this is what make fmt runs).
run "the gofumpt-only difference is still there before --write" 1 "p/strict.go"
run_args "--write formats the unformatted file and names it" 0 "p/strict.go" --write
run "the check passes after --write" 0 ""
if [ "$(cat "$repo/p/strict.go")" != "$(printf 'package p\n\nfunc K() int {\n\treturn 4\n}')" ]; then
  bad "--write did not leave the file as gofumpt writes it"
else
  ok
fi
printf 'package p\n\nfunc H() int {\nreturn 3\n}\n' > "$repo/gen/go/x.go"
run_args "--write leaves gitignored generated code alone" 0 "formatted" --write
if [ "$(cat "$repo/gen/go/x.go")" != "$(printf 'package p\n\nfunc H() int {\nreturn 3\n}')" ]; then
  bad "--write changed gitignored generated code"
else
  ok
fi

# The check must not pass by seeing nothing.
rm -rf "$repo"
mkdir -p "$repo"
git -C "$repo" init -q
printf 'notes\n' > "$repo/README.md"
run "a repo with no Go files fails instead of passing" 1 "no Go files"

nogit="$work/not-a-repo"
rm -rf "$nogit"
mkdir -p "$nogit/p"
printf 'package p\n\nfunc F() int { return 1 }\n' > "$nogit/p/ok.go"
out="$(FLEET_GOFUMPT_REPO_ROOT="$nogit" FLEET_GOFUMPT="$bin" "$check" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
  bad "a directory that is not a git repo passed: $out"
else
  case "$out" in
    *"could not list"*) ok ;;
    *) bad "a directory that is not a git repo: output lacks 'could not list' ($out)" ;;
  esac
fi

printf 'test-check-gofumpt: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
