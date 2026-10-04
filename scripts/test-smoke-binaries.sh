#!/usr/bin/env bash
#
# test-smoke-binaries.sh: seeded RED/GREEN cases for scripts/smoke-binaries.sh.
#
# Builds throwaway Go modules (never this one) whose cmd/ binaries behave well
# or badly. Needs the local Go toolchain. Runs on the macOS system bash 3.2.
#
# Usage: scripts/test-smoke-binaries.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
smoke="$root/scripts/smoke-binaries.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mod="$work/mod"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

# fresh: an empty module.
fresh() {
  rm -rf "$mod"
  mkdir -p "$mod/cmd"
  printf 'module example.com/smoke\n\ngo 1.21\n' > "$mod/go.mod"
}

# binary <name> <go statement run by main>: add cmd/<name>.
binary() {
  mkdir -p "$mod/cmd/$1"
  printf 'package main\n\nimport (\n\t"fmt"\n\t"os"\n)\n\nfunc main() {\n\t_, _ = fmt.Sprint(), os.Args\n\t%s\n}\n' "$2" > "$mod/cmd/$1/main.go"
}

# expect <name> <want rc> <substring of the output, or empty>
expect() {
  local name="$1" want="$2" sub="$3" out rc
  out="$(FLEET_SMOKE_REPO_ROOT="$mod" "$smoke" 2>&1)"
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

fresh
binary good 'fmt.Println("good dev")'
binary also 'fmt.Println("also v1.2.3")'
expect "binaries that print their name pass" 0 "2 binaries"

fresh
binary good 'fmt.Println("good dev")'
binary wrong 'fmt.Println("someone-else dev")'
expect "a binary that prints another name fails and is named" 1 "wrong"

fresh
binary crash 'os.Exit(3)'
expect "a binary that exits non-zero fails and is named" 1 "crash"

fresh
binary silent '_ = 0'
expect "a binary that prints nothing fails" 1 "silent"

fresh
mkdir -p "$mod/cmd/broken"
printf 'package main\n\nfunc main() { undefined() }\n' > "$mod/cmd/broken/main.go"
expect "a binary that does not build fails and is named" 1 "broken"

fresh
expect "no cmd binaries at all fails" 1 "no binaries"

printf 'test-smoke-binaries: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
