#!/usr/bin/env bash
#
# test-nightly-should-run.sh: seeded RED/GREEN cases for
# scripts/nightly-should-run.sh, which decides whether a nightly run tests
# anything (#107). A fake `gh` first on PATH answers the list of earlier
# scheduled runs, so no case reaches GitHub. Cases: main still at the commit the
# last scheduled night tested -> skip; main moved -> run; a failed night at the
# same commit -> skip (its ci-failure issue tracks it); a cancelled night does
# not count; Sundays and non-schedule events run without asking; a failing gh,
# an empty list, a garbled answer or a bad HEAD_SHA -> run, saying why.
# Runs on the macOS system bash 3.2. Needs jq.
#
# Usage: scripts/test-nightly-should-run.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/scripts/nightly-should-run.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
  [ -n "${out:-}" ] && printf '  output: %s\n' "$out" >&2
}

mkdir -p "$work/bin"
cat > "$work/bin/gh" <<'FAKE'
#!/usr/bin/env bash
printf 'gh %s\n' "$*" >> "$FAKE_CALLS"
if [ -n "${FAKE_FAIL:-}" ]; then echo "fake gh: failed" >&2; exit 1; fi
printf '%s\n' "${FAKE_RUNS:-[]}"
FAKE
chmod +x "$work/bin/gh"

head_a=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
head_b=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

# check <name> <want run=true|false> <summary substring> [VAR=value ...]: runs
# the script on a Tuesday for a scheduled event at head_a unless overridden.
check() {
  local name="$1" want="$2" sub="$3" got
  shift 3
  : > "$work/calls"
  : > "$work/output"
  : > "$work/summary"
  out="$(env PATH="$work/bin:$PATH" FAKE_CALLS="$work/calls" GH_REPO=o/r \
    GITHUB_OUTPUT="$work/output" GITHUB_STEP_SUMMARY="$work/summary" \
    EVENT_NAME=schedule HEAD_SHA="$head_a" NIGHTLY_DAY_OF_WEEK=2 "$@" "$script" 2>&1)"
  rc=$?
  got="$(cat "$work/output")"
  if [ "$rc" -ne 0 ]; then
    bad "$name: exit $rc, want 0"
  elif [ "$got" != "run=$want" ]; then
    bad "$name: GITHUB_OUTPUT is '$got', want 'run=$want'"
  elif ! grep -q -F -- "$sub" "$work/summary"; then
    bad "$name: the summary lacks '$sub' ($(cat "$work/summary"))"
  else
    ok
  fi
}

same="[{\"headSha\":\"$head_a\",\"conclusion\":\"success\"}]"
moved="[{\"headSha\":\"$head_b\",\"conclusion\":\"success\"}]"

check "main did not move" false "Skipped" FAKE_RUNS="$same"
check "main moved" true "main moved" FAKE_RUNS="$moved"
check "a failed night at the same commit still counts" false "Skipped" \
  FAKE_RUNS="[{\"headSha\":\"$head_a\",\"conclusion\":\"failure\"}]"
check "a cancelled night does not count (the one before it decides)" false "Skipped" \
  FAKE_RUNS="[{\"headSha\":\"$head_b\",\"conclusion\":\"cancelled\"},{\"headSha\":\"$head_a\",\"conclusion\":\"success\"}]"
check "only cancelled nights: nothing known, so run" true "no earlier" \
  FAKE_RUNS="[{\"headSha\":\"$head_a\",\"conclusion\":\"cancelled\"}]"
check "no earlier scheduled night" true "no earlier" FAKE_RUNS="[]"

# The lookup asks for finished scheduled runs of this workflow on main.
check "lookup arguments" false "Skipped" FAKE_RUNS="$same"
for arg in "run list" "--workflow nightly.yml" "--event schedule" "--branch main" "--status completed" "--json headSha,conclusion"; do
  if grep -q -F -- "$arg" "$work/calls"; then ok; else bad "lookup: gh was not given '$arg'"; fi
done

# Sundays and non-schedule events run without a lookup.
check "Sunday" true "Sunday" FAKE_RUNS="$same" NIGHTLY_DAY_OF_WEEK=7
if [ -s "$work/calls" ]; then bad "Sunday: gh was called"; else ok; fi
check "workflow_dispatch" true "workflow_dispatch" FAKE_RUNS="$same" EVENT_NAME=workflow_dispatch
check "pull_request" true "pull_request" FAKE_RUNS="$same" EVENT_NAME=pull_request
if [ -s "$work/calls" ]; then bad "pull_request: gh was called"; else ok; fi

# RED: an unknown answer is never a skip.
check "gh fails" true "could not list" FAKE_RUNS="$same" FAKE_FAIL=1
check "garbled answer" true "not JSON" FAKE_RUNS="not json"
check "bad HEAD_SHA" true "HEAD_SHA" FAKE_RUNS="$same" HEAD_SHA="x; touch pwned"
if [ -e pwned ] || [ -e "$work/pwned" ]; then bad "bad HEAD_SHA: it ran part of the value"; else ok; fi
check "empty HEAD_SHA" true "HEAD_SHA" FAKE_RUNS="[]" HEAD_SHA=

printf 'test-nightly-should-run: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
