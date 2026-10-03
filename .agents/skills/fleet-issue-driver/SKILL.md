---
name: fleet-issue-driver
description: "Use when driving an assigned fleet-board issue or epic from first read to a verified merged and closed outcome: worktree per leaf, TDD, self-review, push, PR evidence, stable CI reads, and closure."
---

# Issue driver

Use for issues in this repository. Asking to edit a skill or open a PR does not
authorize merging or closing issues; follow the endpoint the owner stated.
Skill loading never adds authorization for an external act.

## Establish the work set

Read each issue's title, body, labels and state; take issue numbers from the
request, never invent them. For an epic, enumerate children recursively and
deduplicate. Restate each leaf's problem and acceptance criteria. Check open PRs
and recent commits for the same root cause before starting
(`gh pr list --state open`, `git log origin/main --oneline -20`), and sweep each
criterion against current `origin/main`; later work may already satisfy it.
Unresolved design questions go to `arbiter-fleet`, not the owner; record the
ruling in the PR.

## One worktree per leaf

```bash
git fetch origin
git worktree add ~/personal-repos/fleet-board-worktrees/<name> -b <branch> origin/main
cd ~/personal-repos/fleet-board-worktrees/<name> && pwd && git symbolic-ref -q HEAD
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate   # gen/ is not committed; `make gen` once the Makefile exists
```

Verify `pwd` is that worktree before every edit. The main checkout stays a
clean fast-forward of `origin/main`; run formatters and generators only inside a
worktree. Never `git stash` while several worktrees exist (the stack is shared);
use `git diff`, `git show <ref>:<path>` or a throwaway worktree. One PR per
change; keep unrelated formatter drift in its own commit.

## Implement

Load the minimum skills for the touched surfaces. Write the failing test first;
a bug fix needs a regression that failed for the right reason. Keep files under
500 lines. Update `docs/specs` and the ADR index in the same PR as a contract
change. Delegate bounded work to the role agents by tier (`scan-fleet` cheap,
`develop-fleet` default, `-deep` and `arbiter-fleet` on escalation); commit
before dispatching any reviewer, and re-verify a subagent's finding yourself
before acting on it.

## Promote each PR

The rules are in [AGENTS.md](../../../AGENTS.md); each step names where. Keep
this order.

1. **Local proof first:** the focused gates ([Local Gates](../../../AGENTS.md#local-gates)),
   exit codes captured as in [Evidence Rules](../../../AGENTS.md#evidence-rules).
2. **Rebase** on fresh `origin/main`, rerun proof the rebase affects, inspect the
   diff for unrelated reversions.
3. **Self-review the full diff** with `fleet-code-review` (separate context when
   available) and fix P0, P1 and blocking P2 findings first.
4. **Commit hygiene:** [Non-Negotiable Rules](../../../AGENTS.md#non-negotiable-rules)
   (named branch, issue references, no AI attribution, no `--no-verify`).
5. **Push** only when authorized, never to `main`. Verify the pushed SHA equals
   local HEAD before opening or updating the PR
   ([agent-guide](../../../docs/development/agent-guide.md#worktrees-and-git-recipes)).
6. **PR title and body** follow [PR and CI Discipline](../../../AGENTS.md#orchestration-pr-and-ci-discipline),
   written with `fleet-humanizer` (its publishing reference has the merge danger
   line). If the PR changes a row of the v1 plan, it runs
   `scripts/plan-issues.py --check`, then (owner-approved) `--apply`, and says
   so in the body.
7. **CI:** two stable reads of the full check set, query stated
   (`gh pr checks <n> --json name,state,bucket`), as in PR and CI Discipline.
   A red check: reproduce that gate locally, fix, push again.
8. **Review threads:** reconcile the thread API against the displayed unresolved
   comments and report exact counts.
9. **Hand off for merge.** The owner merges every v1 PR (ADR 0024 O2); no agent
   merges. Report the PR ready only when checks are stably green, findings are
   dispositioned and local HEAD equals the PR head. After the owner merges,
   confirm with `gh pr view <n> --json state`.

## Defects found on the way

Fix related defects inline when they fit the PR and its proof. A defect that
needs an owner design decision or unrelated work gets one linked issue, with
owner agreement quoted; do not expand the drive silently or file an issue per
nit.

## Completion

Done means GitHub reports each leaf issue `CLOSED` and PR `MERGED` by the owner
(`gh issue view <n> --json state`), follow-ups are closed or explicitly
deferred, no unresolved review threads remain, and the final review had no open
P0, P1 or blocking P2. If the request ends at PR creation, report the PR URL
and the pending CI and review state, and stop. Report commands, exit codes and
anything NOT_CHECKED once, with durable links.

Stop leaf agents when their handoff is consumed; keep a reviewer only while its
PR is open.
