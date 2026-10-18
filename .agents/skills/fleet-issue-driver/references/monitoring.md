# Monitor owned PRs

Start or refresh a live watcher in the same turn as PR creation or a follow-up
push. Track only PRs owned by this task's branches/worktrees; the shared GitHub
account's author filter is not ownership. One watcher can cover all of the
coordinator's PRs. Add newly created PRs immediately. Children hand off commits
and evidence; the coordinator publishes and owns the watcher. A peer coordinator
owns its own PRs; do not take them over.

Poll about every 60 seconds with sparse state-change output. Use the harness's
native monitor when available, otherwise a live background process with its
PID and log path recorded. A prose promise to watch is not a watcher. Keep it
running while outside work blocks progress; if it dies, restart it or report
the failure. Stop it once the task reaches its authorized endpoint.

Each poll must cover:

- The current head SHA, all check results and required-check status. Treat a
  cancelled required check as unfinished. Confirm requirements in GitHub;
  never infer that an absent check is optional. Re-read the head after the
  queries and discard the sample if it changed.
- Merge conflicts. Retry an unknown mergeability result; if it remains unknown
  after the next poll, inspect and report rather than calling it mergeable.
- New review bodies, issue comments and inline comments/threads from every
  author, including the owner, bots and the session itself. Process actual
  feedback; a quota notice or automated status post is not a completed review.

Use `gh pr view <n> --json headRefOid,mergeable,reviews` and
`gh pr checks <n> --json name,state,bucket`, plus paginated
`gh api repos/OWNER/REPO/issues/<n>/comments` and
`gh api repos/OWNER/REPO/pulls/<n>/comments`. Reconcile the GraphQL
`reviewThreads` connection (paginate it) against displayed unresolved comments.
Record the exact query and unresolved count in the final evidence.

Two consecutive reads of the full check set must have no pending checks, the
same total and the same head. Green requires every required check to pass;
stable failures are not success. Reproduce failures locally, fix, re-run proof
and publish within the existing authorization. Review each finding against
current code, fix before replying, and resolve each thread only after its
finding is addressed. A silent or unavailable reviewer is not evidence of a
completed review. The owner merges every v1 PR.

Honor an explicit PR-creation-only endpoint: hand off its URL and current CI
and review state, then stop the watcher. Otherwise report an outside wait as
`BLOCKED: <reason> WATCH=<pid>` with the log path. A watcher grants no merge,
publication, new-scope or account-change permission.
