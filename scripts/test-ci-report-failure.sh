#!/usr/bin/env bash
#
# test-ci-report-failure.sh: seeded RED/GREEN cases for
# scripts/ci-report-failure.sh, which keeps one open `ci-failure` issue per
# workflow in step with that workflow's runs on main (#107).
#
# A fake `gh` first on PATH records every call and answers from env vars, so no
# case reaches GitHub. Cases: no issue -> opens one; an open one -> comments;
# green -> closes it; green after main moved on -> leaves it; another
# workflow's issue is never touched; the body holds the run URL, the SHA and
# the failed job names and nothing else (never log text); bad input and a
# failing gh fail the script.
# Runs on the macOS system bash 3.2. Needs jq.
#
# Usage: scripts/test-ci-report-failure.sh        exit 0 only if every case passes
set -u

root="$(cd "$(dirname "$0")/.." && pwd)"
script="$root/scripts/ci-report-failure.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

pass=0
fail=0
ok() { pass=$((pass + 1)); }
bad() {
  fail=$((fail + 1))
  printf 'FAIL: %s\n' "$1" >&2
  [ -n "${out:-}" ] && printf '  output: %s\n' "$out" >&2
  [ -f "$work/calls" ] && sed 's/^/  gh: /' "$work/calls" >&2
}

mkdir -p "$work/bin"
cat > "$work/bin/gh" <<'FAKE'
#!/usr/bin/env bash
# Fake gh: one line per call (arguments joined by " | ", newlines as \n), and
# the raw --body of the last call that had one in $FAKE_CALLS.body. Answers
# come from FAKE_*.
nl='
'
{
  printf 'gh'
  for a in "$@"; do printf ' | %s' "${a//$nl/\\n}"; done
  printf '\n'
} >> "$FAKE_CALLS"
prev=""
for a in "$@"; do
  if [ "$prev" = "--body" ]; then printf '%s' "$a" > "$FAKE_CALLS.body"; fi
  prev="$a"
done
default_labels='[{"name":"ci-failure"}]'
case "$1 ${2:-}" in
  "${FAKE_FAIL_ON:-none}") echo "fake gh: $1 $2 failed" >&2; exit 1 ;;
esac
case "$1 ${2:-}" in
  "issue list") printf '%s\n' "${FAKE_ISSUES:-[]}" ;;
  "label list") printf '%s\n' "${FAKE_LABELS-$default_labels}" ;;
  "issue create") echo "https://github.com/o/r/issues/99" ;;
  api*) printf '{"sha":"%s"}\n' "${FAKE_MAIN_SHA:-}" ;;
esac
exit 0
FAKE
chmod +x "$work/bin/gh"

sha_a=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
sha_b=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
run_url="https://github.com/o/r/actions/runs/123"
failed='{"gates":{"result":"failure","outputs":{}},"lint":{"result":"success","outputs":{}}}'
green='{"gates":{"result":"success","outputs":{}}}'
open_ci='[{"number":7,"title":"CI failure: ci on main"}]'
open_other='[{"number":8,"title":"CI failure: nightly on main"}]'

# run <needs json> [VAR=value ...]: runs the script with the fake gh and the
# usual env (workflow ci, sha_a, run_url); extra VAR=value pairs override it.
run() {
  local needs="$1"
  shift
  : > "$work/calls"
  rm -f "$work/calls.body"
  rm -rf "$work/case"
  mkdir -p "$work/case"
  out="$(cd "$work/case" && env PATH="$work/bin:$PATH" FAKE_CALLS="$work/calls" GH_REPO=o/r \
    WORKFLOW=ci RUN_URL="$run_url" SHA="$sha_a" NEEDS_JSON="$needs" \
    FAKE_MAIN_SHA="$sha_a" "$@" "$script" 2>&1)"
  rc=$?
}

called() { grep -q -F -- "$1" "$work/calls"; }
calls_count() { wc -l < "$work/calls" | tr -d ' '; }

# A failure with no open issue opens one, titled for the workflow and labelled.
run "$failed"
if [ "$rc" -ne 0 ]; then
  bad "open: exit $rc, want 0"
elif ! called "gh | issue | create | --title | CI failure: ci on main | --label | ci-failure"; then
  bad "open: no issue create with the workflow title and the ci-failure label"
elif called "issue | comment" || called "issue | close"; then
  bad "open: commented or closed instead of only opening"
else
  ok
fi

# The body is exactly this: the run URL, the SHA and the failed job names. A
# job that passed is not named, and nothing comes from the run's logs.
want_body="The \`ci\` workflow failed on main.

- Run: $run_url
- Commit: $sha_a
- Failed jobs: gates

The run page has the logs. This issue closes on the next green \`ci\` run on main."
body="$(cat "$work/calls.body" 2>/dev/null)"
if [ "$body" != "$want_body" ]; then
  bad "body: got <<$body>>, want <<$want_body>>"
elif called "run | view" || called "--log"; then
  bad "body: the script read the run's logs"
else
  ok
fi

# Two failed jobs are both named, in a stable order.
run '{"lint":{"result":"failure"},"gates":{"result":"failure"}}'
if [ "$rc" -eq 0 ] && grep -q -F -- "- Failed jobs: gates, lint" "$work/calls.body"; then ok; else bad "two failed jobs: not both named in order (exit $rc)"; fi

# A missing label is created first; an existing one is not.
run "$failed" FAKE_LABELS='[]'
if [ "$rc" -eq 0 ] && called "gh | label | create | ci-failure"; then ok; else bad "label: missing label not created (exit $rc)"; fi
run "$failed"
if called "label | create"; then bad "label: created although it exists"; else ok; fi

# A failure with the issue already open comments on it and opens nothing.
run "$failed" FAKE_ISSUES="$open_ci"
if [ "$rc" -ne 0 ]; then
  bad "comment: exit $rc, want 0"
elif ! called "gh | issue | comment | 7 | --body"; then
  bad "comment: no comment on issue 7"
elif called "issue | create"; then
  bad "comment: opened a second issue"
else
  ok
fi

# Another workflow's open issue is not this one: a ci failure opens its own.
run "$failed" FAKE_ISSUES="$open_other"
if called "issue | create" && ! called "| 8 |"; then ok; else bad "other workflow: touched issue 8 or opened nothing"; fi

# Green with the issue open, at main's tip, closes it with the run URL.
run "$green" FAKE_ISSUES="$open_ci"
if [ "$rc" -ne 0 ]; then
  bad "close: exit $rc, want 0"
elif ! called "gh | issue | close | 7 | --comment"; then
  bad "close: issue 7 not closed"
elif ! grep -F "issue | close" "$work/calls" | grep -q -F -- "$run_url"; then
  bad "close: the closing comment lacks the run URL"
else
  ok
fi

# Green for a commit main has moved past leaves the issue to the newer run.
run "$green" FAKE_ISSUES="$open_ci" FAKE_MAIN_SHA="$sha_b"
if [ "$rc" -eq 0 ] && ! called "issue | close"; then ok; else bad "stale green: closed the issue (exit $rc)"; fi

# Green with nothing open does nothing that writes.
run "$green"
if [ "$rc" -eq 0 ] && ! called "issue | create" && ! called "issue | close" && ! called "issue | comment"; then ok; else bad "green, none open: wrote something (exit $rc)"; fi

# A cancelled or skipped job is neither a failure nor green: no gh call at all.
run '{"gates":{"result":"cancelled","outputs":{}}}' FAKE_ISSUES="$open_ci"
if [ "$rc" -eq 0 ] && [ "$(calls_count)" -eq 0 ]; then ok; else bad "cancelled: exit $rc or gh was called"; fi
run '{"suite":{"result":"skipped","outputs":{}}}'
if [ "$rc" -eq 0 ] && [ "$(calls_count)" -eq 0 ]; then ok; else bad "skipped: exit $rc or gh was called"; fi

# RED: bad input fails before any gh call.
for case in "SHA=not-a-sha" "SHA=${sha_a}; touch pwned" "RUN_URL=https://evil.example/x" \
  "WORKFLOW=ci\$(touch pwned)" "NEEDS_JSON=" "NEEDS_JSON=not json" "NEEDS_JSON={}" \
  'NEEDS_JSON={"bad name;x":{"result":"failure"}}' "GH_REPO=o/r;x"; do
  run "$failed" "$case"
  if [ "$rc" -eq 0 ]; then
    bad "bad input '$case': exit 0"
  elif [ "$(calls_count)" -ne 0 ]; then
    bad "bad input '$case': gh was called"
  elif [ -n "$(ls -A "$work/case")" ]; then
    bad "bad input '$case': it ran part of the value"
  else
    ok
  fi
done

# RED: a gh failure fails the script, so the reporter job goes red.
run "$failed" FAKE_FAIL_ON="issue list"
if [ "$rc" -ne 0 ]; then ok; else bad "gh issue list failing: exit 0"; fi
run "$failed" FAKE_FAIL_ON="issue create"
if [ "$rc" -ne 0 ]; then ok; else bad "gh issue create failing: exit 0"; fi
run "$green" FAKE_ISSUES="$open_ci" FAKE_FAIL_ON="issue close"
if [ "$rc" -ne 0 ]; then ok; else bad "gh issue close failing: exit 0"; fi

# RED: an answer gh garbled is an error, not "no issue" or "no label".
run "$failed" FAKE_ISSUES="not json"
if [ "$rc" -ne 0 ] && ! called "issue | create"; then ok; else bad "garbled issue list: exit $rc or opened an issue"; fi
run "$failed" FAKE_LABELS="not json"
if [ "$rc" -ne 0 ] && ! called "label | create"; then ok; else bad "garbled label list: exit $rc or created the label"; fi

printf 'test-ci-report-failure: %d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
