#!/usr/bin/env bash
#
# test-make-test-flags.sh: seeded RED/GREEN cases for the flags `make test`
# hands to go test (CI decision 3, #107): a first pass with -shuffle=on and
# -count=$(TEST_COUNT) (default 3), then a -race -shuffle=on -count=1 pass.
#
# Builds a throwaway git repo (never this one) holding a copy of the Makefile,
# tool-versions.env and scripts/changed-go-packages.sh, plus a tiny Go module:
#   clean  a test with no shared state                      (GREEN)
#   probe  a test that writes the test.shuffle and test.count flags it got,
#          whether the race detector is on, and the GOFLAGS it saw, to
#          $FLEET_TEST_FLAGS_LOG
#   state  a test that counts its runs in a package-level variable: it passes
#          at -count=1 and must fail under `make test`      (RED)
# The packages are added on a work branch, so changed-go-packages.sh names them.
# TEST_COUNT is data like BASE_REF: a value that is not a positive integer
# fails before go runs, and is never shell text. `make test-all` (the nightly
# suite) is checked the same way: one -race pass over every package.
# Runs on the macOS system bash 3.2. Needs make, git and go.
#
# Usage: scripts/test-make-test-flags.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
repo="$work/repo"
flags_log="$work/flags.log"

pass=0
fail=0

ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

# Keep git and make from reading the caller's repo, config or flags.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE BASE_REF FLEET_BASE_REF GOFLAGS TEST_COUNT
unset MAKEFLAGS MFLAGS MAKEOVERRIDES MAKELEVEL MAKEFILES GNUMAKEFLAGS
# Nor the caller's go.work or go env file (GOFLAGS there would reach go test).
export GOWORK=off GOENV=off
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export FLEET_TEST_FLAGS_LOG="$flags_log"

g() { git -C "$repo" "$@"; }

# fresh: a base commit (Makefile and module, no packages) as origin/main, then
# a work branch that adds the clean and probe packages.
fresh() {
  rm -rf "$repo" "$flags_log"
  mkdir -p "$repo/scripts"
  g init -q -b main
  cp "$root/Makefile" "$root/tool-versions.env" "$repo/"
  cp "$root/scripts/changed-go-packages.sh" "$repo/scripts/"
  printf 'module example.com/m\n\ngo 1.21\n' > "$repo/go.mod"
  g add -A
  g commit -q -m base
  g update-ref refs/remotes/origin/main HEAD
  g checkout -q -b work

  mkdir -p "$repo/clean" "$repo/probe"
  cat > "$repo/clean/clean_test.go" <<'EOF'
package clean

import "testing"

func TestSum(t *testing.T) {
	if got := Sum(2, 3); got != 5 {
		t.Fatalf("Sum(2, 3) = %d, want 5", got)
	}
}
EOF
  printf 'package clean\n\n// Sum adds.\nfunc Sum(a, b int) int { return a + b }\n' > "$repo/clean/clean.go"
  cat > "$repo/probe/probe_test.go" <<'EOF'
package probe

import (
	"flag"
	"fmt"
	"os"
	"testing"
)

func TestRecordFlags(t *testing.T) {
	f, err := os.OpenFile(os.Getenv("FLEET_TEST_FLAGS_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, "shuffle=%s count=%s race=%t goflags=%s\n",
		flag.Lookup("test.shuffle").Value, flag.Lookup("test.count").Value, raceOn, os.Getenv("GOFLAGS"))
}
EOF
  printf '//go:build !race\n\npackage probe\n\nconst raceOn = false\n' > "$repo/probe/norace_test.go"
  printf '//go:build race\n\npackage probe\n\nconst raceOn = true\n' > "$repo/probe/race_test.go"
  g add -A
  g commit -q -m "clean and probe"
}

add_state() {
  mkdir -p "$repo/state"
  cat > "$repo/state/state_test.go" <<'EOF'
package state

import "testing"

var runs int

func TestRunsOnce(t *testing.T) {
	runs++
	if runs > 1 {
		t.Fatalf("state leaked: run %d sees the count of an earlier run", runs)
	}
}
EOF
  g add -A
  g commit -q -m "state"
}

# make_test <name> <want rc> <make args...>: runs `make test` in the scratch
# repo; the output is left in $out.
make_test() {
  local name="$1" want="$2" rc
  shift 2
  out="$(cd "$repo" && make --no-print-directory test "$@" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: make test exit $rc, want $want ($out)"
    return 1
  fi
  ok
}

# flags_are <name> <expected log, newline separated>
flags_are() {
  local name="$1" want="$2" got
  got="$(sort "$flags_log" 2>/dev/null)"
  want="$(printf '%s\n' "$want" | sort)"
  if [ "$got" = "$want" ]; then ok; else bad "$name: the tests got '$got', want '$want'"; fi
}

contains() {
  local name="$1" sub="$2"
  case "$out" in
    *"$sub"*) ok ;;
    *) bad "$name: output lacks '$sub' ($out)" ;;
  esac
}

# GREEN: clean tests pass, and each pass got the flags it should.
fresh
make_test "clean packages pass" 0
flags_are "default flags reach go test" "shuffle=on count=3 race=false goflags=
shuffle=on count=3 race=false goflags=
shuffle=on count=3 race=false goflags=
shuffle=on count=1 race=true goflags="

# The pre-push hook runs make with GOFLAGS=-count=1; the command line wins.
fresh
GOFLAGS=-count=1 make_test "clean packages pass under the pre-push GOFLAGS" 0
flags_are "GOFLAGS=-count=1 does not lower the count" "shuffle=on count=3 race=false goflags=-count=1
shuffle=on count=3 race=false goflags=-count=1
shuffle=on count=3 race=false goflags=-count=1
shuffle=on count=1 race=true goflags=-count=1"

# RED: package-level state passes at -count=1 but fails under make test.
fresh
add_state
rc=0
(cd "$repo" && go test -count=1 ./state > "$work/once.out" 2>&1) || rc=$?
if [ "$rc" -eq 0 ]; then ok; else bad "the seeded state test should pass at -count=1: exit $rc ($(cat "$work/once.out"))"; fi
if make_test "a test that keeps package-level state fails" 2; then
  contains "the failure names the leaked state" "state leaked: run 2"
  contains "the shuffle seed line shows the flag reached go test" "-test.shuffle "
fi

# TEST_COUNT is honored: at 1 the state test passes again.
fresh
add_state
make_test "TEST_COUNT=1 runs each test once" 0 TEST_COUNT=1
flags_are "TEST_COUNT=1 reaches go test" "shuffle=on count=1 race=false goflags=
shuffle=on count=1 race=true goflags="

# RED: a TEST_COUNT that is not a positive integer fails before go runs and is
# never run as shell text (it would turn make test into a green no-op).
marker="$work/marker"
for hostile in "1; touch $marker" "1; exit 0 #" "0" "3 -run=^\$" "3 -run=NoSuch"; do
  fresh
  add_state
  rm -f "$marker"
  if make_test "TEST_COUNT='$hostile' fails" 2 TEST_COUNT="$hostile"; then
    contains "TEST_COUNT='$hostile' is named" "TEST_COUNT must be a positive integer"
  fi
  if [ -e "$marker" ]; then bad "TEST_COUNT='$hostile' ran as shell text"; else ok; fi
done

# COVERPROFILE (CI sets it): the non-race pass writes the coverage profile, so
# its mode is `set` (a -race pass would write `atomic`). Unset, none is written.
# The value is a path, never shell text.
fresh
make_test "make test without COVERPROFILE" 0
if ls "$repo"/*.out > /dev/null 2>&1; then bad "no COVERPROFILE: a profile was written"; else ok; fi
fresh
if make_test "make test with COVERPROFILE" 0 COVERPROFILE="$work/cover.out"; then
  if ! head -n 1 "$work/cover.out" 2> /dev/null | grep -qx 'mode: set'; then
    bad "COVERPROFILE: want a 'mode: set' profile from the non-race pass, got '$(head -n 1 "$work/cover.out" 2> /dev/null)'"
  elif ! grep -q '^example.com/m/clean/clean.go:' "$work/cover.out"; then
    bad "COVERPROFILE: the profile has no block for clean/clean.go"
  else
    ok
  fi
fi
fresh
rm -f "$repo/pwned"
make_test "a COVERPROFILE with shell text is a file name" 0 COVERPROFILE="c.out; touch pwned"
if [ -e "$repo/pwned" ]; then bad "COVERPROFILE ran as shell text"; else ok; fi

# make test-all (the nightly suite): one -race pass over every package, the
# packages main already had included, TEST_COUNT times; no non-race pass.
make_test_all() {
  local name="$1" want="$2" rc
  shift 2
  out="$(cd "$repo" && make --no-print-directory test-all "$@" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name: make test-all exit $rc, want $want ($out)"
    return 1
  fi
  ok
}
fresh
g checkout -q main
g merge -q --ff-only work
make_test_all "test-all on main (nothing changed) still runs every package" 0 TEST_COUNT=2
flags_are "test-all: race, shuffled, TEST_COUNT times" "shuffle=on count=2 race=true goflags=
shuffle=on count=2 race=true goflags="

fresh
g checkout -q main
g merge -q --ff-only work
if make_test_all "test-all with COVERPROFILE" 0 TEST_COUNT=1 COVERPROFILE="$work/all.out"; then
  if head -n 1 "$work/all.out" 2> /dev/null | grep -qx 'mode: atomic' && grep -q '^example.com/m/clean/clean.go:' "$work/all.out"; then
    ok
  else
    bad "test-all COVERPROFILE: want an 'atomic' profile with clean/clean.go"
  fi
fi

# RED: the leaked-state test fails under test-all at its default count.
fresh
add_state
if make_test_all "test-all catches package-level state" 2; then
  contains "test-all names the leaked state" "state leaked: run 2"
fi

for hostile in "1; touch $marker" "0" "3 -run=NoSuch"; do
  fresh
  rm -f "$marker"
  if make_test_all "test-all TEST_COUNT='$hostile' fails" 2 TEST_COUNT="$hostile"; then
    contains "test-all TEST_COUNT='$hostile' is named" "TEST_COUNT must be a positive integer"
  fi
  if [ -e "$marker" ]; then bad "test-all TEST_COUNT='$hostile' ran as shell text"; else ok; fi
done

printf 'test-make-test-flags: %s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
