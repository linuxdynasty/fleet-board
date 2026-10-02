# 0019. Pre-implementation contract decisions: reuse, sign-in, freeze, log access

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Amends: [0003](0003-transport-grpc.md) (additive-only rule),
  [0004](0004-agent-connectivity.md), [0010](0010-session-logs.md),
  [0017](0017-contract-packages.md), [0018](0018-hub-api-and-contract-amendments.md)

## Context

A second contract review before implementation
([docs/reviews/2026-10-02-contract-review.md](../reviews/2026-10-02-contract-review.md))
confirmed 86 findings. Four of them needed the owner's decision because they
change the product, not just the contract.

## Decision

**1. Session reuse: same repo only.** A goal may reuse an existing harness
pane only when that pane's working directory is a checkout of the goal's
repo. The goal then works on a new branch in that checkout, and only when the
checkout is clean. Otherwise the goal gets a new window in a new worktree. A
running harness cannot change its folder or gain environment variables, so:

- Goal attribution for every harness comes from the **agent's goal
  registry** (pane key, worktree path, harness session id), written by
  `StartGoal` before the goal is typed and removed when the goal ends.
  `FLEET_GOAL_ID` in the pane environment is only a fast path for new windows.
- The agent reports each pane's current directory and, when it is inside an
  allowed root, its repo and worktree, so the hub can pick candidates.

**2. Sign-in: passkeys and GitHub, owner's choice.** `authenticated` mode
offers both, and the owner can enable either or both:

- First run: `fleetd` prints a one-time **claim URL**; whoever opens it
  becomes the owner and registers a passkey or links a GitHub account.
- **Passkeys** (WebAuthn): no passwords stored.
- **GitHub sign-in** through the GitHub App the hub already uses, limited to
  an allow-list of GitHub users.
- The CLI uses **API tokens**; a phone uses a **paired device** (ADR 0018).
- `local_trusted` mode binds to loopback only and needs no sign-in.

**3. Contracts are unfrozen until v0.1.** No code depends on the contracts
yet, so breaking changes (renames, type changes such as consent as an enum)
are allowed until the first implementation release is tagged `v0.1`. From
that tag on, ADR 0003's additive-only rule applies again and `buf breaking`
runs against the tag in CI. Until then `buf build` and `buf lint` must pass
on every change.

**4. Terminal logs: owner by default, explicit grant otherwise.** Reading
stored session logs, transcripts and archived pane summaries needs a separate
`LOGS` scope. Owners have it; a viewer, API token or paired device has it only
when the owner grants it. This keeps ADR 0010's "owner only" rule while
allowing a deliberate exception.

## Consequences

- StartGoal gains a registration step and a clean-checkout check for reused
  panes; preflight can refuse with a workdir mismatch.
- The hub API gains sign-in, claim, passkey, GitHub and API-token flows; the
  hub stores no passwords.
- The contract fixes from the review may rename and retype fields; history
  stays readable in git, not in `reserved` lines.
- Phone pairing defaults exclude logs.
