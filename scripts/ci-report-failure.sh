#!/usr/bin/env bash
#
# ci-report-failure.sh: keep one open `ci-failure` issue per workflow in step
# with that workflow's runs on main (#107). The last job of ci.yml (main pushes)
# and nightly.yml runs it after the others, whatever they did.
#
#   a job failed     open "CI failure: <workflow> on main", or comment on it
#                    when it is already open
#   every job passed close that issue, if open, unless main has moved past
#                    this run's commit (a newer run decides then)
#   anything else    (a cancelled or skipped job) do nothing
#
# The issue text is the run URL, the commit SHA and the failed job names,
# never log text: a log holds whatever a test printed.
#
# Env (all required): WORKFLOW (the workflow name), RUN_URL, SHA, NEEDS_JSON
# (the workflow's ${{ toJSON(needs) }}), GH_REPO (owner/name). gh reads its
# token from GH_TOKEN. Every value is checked before the first gh call and is
# only ever passed as an argument, never run.
# Portable to the macOS system bash 3.2 (scripts/test-ci-report-failure.sh).
set -euo pipefail

label="ci-failure"

die() {
  printf 'ci-report-failure: %s\n' "$1" >&2
  exit 2
}

workflow="${WORKFLOW:-}"
run_url="${RUN_URL:-}"
sha="${SHA:-}"
needs="${NEEDS_JSON:-}"
repo="${GH_REPO:-}"

re_workflow='^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$'
re_run_url='^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[0-9]+(/attempts/[0-9]+)?$'
re_sha='^[0-9a-f]{40}$'
re_repo='^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$'

[[ "$workflow" =~ $re_workflow ]] || die "WORKFLOW is missing or not a plain name"
[[ "$run_url" =~ $re_run_url ]] || die "RUN_URL is not a GitHub Actions run URL"
[[ "$sha" =~ $re_sha ]] || die "SHA is not a 40-character commit SHA"
[[ "$repo" =~ $re_repo ]] || die "GH_REPO is not owner/name"
printf '%s' "$needs" | jq -e 'type == "object" and length > 0' > /dev/null 2>&1 ||
  die "NEEDS_JSON is not a non-empty JSON object"
printf '%s' "$needs" | jq -e 'keys | all(test("^[A-Za-z0-9_-]+$"))' > /dev/null ||
  die "NEEDS_JSON names a job that is not a plain id"

failed="$(printf '%s' "$needs" | jq -r '[to_entries[] | select(.value.result == "failure") | .key] | sort | join(", ")')"
all_green="$(printf '%s' "$needs" | jq -r '[.[].result] | all(. == "success")')"

if [ -z "$failed" ] && [ "$all_green" != "true" ]; then
  echo "ci-report-failure: no job failed and not every job passed (cancelled or skipped); nothing to do"
  exit 0
fi

title="CI failure: $workflow on main"
# The REST issue list, not `gh issue list --label` (search, whose index lags
# writes): an issue the previous report just opened must be found. It also
# lists pull requests, which are skipped. gh exits non-zero on an API error,
# and set -e stops here then.
issues="$(gh api "repos/$repo/issues?state=open&labels=$label&per_page=100")"
issue="$(printf '%s' "$issues" | jq -r --arg t "$title" \
  'map(select(.pull_request == null and .title == $t)) | .[0].number // empty')"

if [ -n "$failed" ]; then
  body="The \`$workflow\` workflow failed on main.

- Run: $run_url
- Commit: $sha
- Failed jobs: $failed

The run page has the logs. This issue closes on the next green \`$workflow\` run on main."
  if [ -n "$issue" ]; then
    gh issue comment "$issue" --body "$body"
    echo "ci-report-failure: commented on #$issue"
  else
    # --force succeeds whether or not the label exists, so the first reports
    # of two workflows at once cannot fail each other.
    gh label create "$label" --force --color B60205 \
      --description "A CI run on main failed (scripts/ci-report-failure.sh)"
    gh issue create --title "$title" --label "$label" --body "$body"
  fi
  exit 0
fi

if [ -z "$issue" ]; then
  echo "ci-report-failure: green, and no open \"$title\" issue"
  exit 0
fi
head="$(gh api "repos/$repo/commits/main" | jq -r '.sha')"
if [ "$head" != "$sha" ]; then
  echo "ci-report-failure: green at $sha, but main is at $head; the run for main's tip closes #$issue"
  exit 0
fi
gh issue close "$issue" --comment "Green again on main: $run_url at $sha."
echo "ci-report-failure: closed #$issue"
