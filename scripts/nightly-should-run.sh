#!/usr/bin/env bash
#
# nightly-should-run.sh: decide whether a nightly run tests anything (#107).
# The check job of .github/workflows/nightly.yml runs it.
#
# A scheduled night is skipped when main's HEAD is the commit the last finished
# scheduled nightly on main tested. That run counts whatever its conclusion
# except cancelled: a red main is tracked by its ci-failure issue, not re-run
# every night. Sundays (UTC) and every other event (workflow_dispatch,
# pull_request) always run. An unknown answer (gh fails, no earlier run, a
# garbled list, a bad HEAD_SHA) runs; unknown never means skip.
#
# Env: EVENT_NAME, HEAD_SHA, GH_REPO (gh reads it, and GH_TOKEN), and the
# GitHub step files GITHUB_OUTPUT (gets run=true or run=false) and
# GITHUB_STEP_SUMMARY (gets the reason). NIGHTLY_DAY_OF_WEEK replaces
# `date -u +%u` (1 to 7, 7 is Sunday) for scripts/test-nightly-should-run.sh.
# Portable to the macOS system bash 3.2.
set -euo pipefail

output="${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set}"
summary="${GITHUB_STEP_SUMMARY:?GITHUB_STEP_SUMMARY is not set}"

decide() {
  printf 'run=%s\n' "$1" >> "$output"
  printf 'nightly: %s\n' "$2" | tee -a "$summary"
  exit 0
}

event="${EVENT_NAME:-}"
head="${HEAD_SHA:-}"
re_sha='^[0-9a-f]{40}$'

[ "$event" = "schedule" ] || decide true "run: started by ${event:-an unknown event}, not the schedule."
[[ "$head" =~ $re_sha ]] || decide true "run: HEAD_SHA is not a commit SHA, so nothing is known to skip."
day="${NIGHTLY_DAY_OF_WEEK:-$(date -u +%u)}"
[ "$day" != "7" ] || decide true "run: Sundays always run."

runs="$(gh run list --workflow nightly.yml --event schedule --branch main --status completed \
  --limit 5 --json headSha,conclusion 2> /dev/null)" ||
  decide true "run: could not list earlier scheduled nightly runs."
last="$(printf '%s' "$runs" | jq -r '[.[] | select(.conclusion != "cancelled")][0].headSha // empty' 2> /dev/null)" ||
  decide true "run: the list of earlier scheduled nightly runs was not JSON."
[ -n "$last" ] || decide true "run: no earlier finished scheduled nightly run."
[ "$last" != "$head" ] || decide false "Skipped: main is still at ${head:0:12}, which the last scheduled nightly tested."
decide true "run: main moved from ${last:0:12} to ${head:0:12} since the last scheduled nightly."
