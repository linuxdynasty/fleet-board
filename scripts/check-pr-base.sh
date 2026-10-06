#!/bin/sh
#
# check-pr-base.sh: fail unless a pull request targets main (#107).
#
# Every PR is based on main, never stacked on another branch: CI measures the
# change against the PR's base, so a PR based on a feature branch would test
# only the top of the stack. The pr-meta workflow runs this on every PR event,
# and the gates job in ci.yml runs it first on a pull request, so gates never
# pass against a wrong base. Changing the base starts no gates run, and a rerun
# of a job reuses the original event data (github.base_ref included), so the
# fix is to retarget, then push again.
#
# The base branch name is author-controlled text. It arrives only through the
# PR_BASE_REF env var (never pasted into the script by ${{ }}), is compared as a
# plain string and printed with printf %s; it is never evaluated.
#
# Usage: PR_BASE_REF=<base branch name> scripts/check-pr-base.sh
# Exit 0: the base is main. Exit 1: any other base, or none.
# scripts/test-check-pr-base.sh holds the seeded cases.

base="${PR_BASE_REF-}"

if [ "$base" = "main" ]; then
  echo "check-pr-base: the pull request targets main."
  exit 0
fi

if [ -z "$base" ]; then
  echo "check-pr-base: no base branch was given (PR_BASE_REF is empty); a pull request must target main." >&2
else
  printf 'check-pr-base: this pull request targets "%s"; it must target main.\n' "$base" >&2
fi
echo "Retarget the pull request to main (on GitHub: Edit, next to the title), then push again." >&2
echo "Rerunning this job does not help: a rerun reuses the old base (GitHub replays the original event, github.base_ref included)." >&2
exit 1
