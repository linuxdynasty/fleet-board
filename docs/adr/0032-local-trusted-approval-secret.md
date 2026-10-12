# 0032. Approvals and settings changes in `local_trusted` mode need a local secret

- Status: Accepted
- Date: 2026-10-12
- Deciders: linuxdynasty, decided by merging the PR that closes #62
- Amends: [0024](0024-pre-build-owner-decisions.md) (decision 20, now decided)
- Builds on: [0019](0019-pre-implementation-contract-decisions.md) (the claim
  URL pattern), [0025](0025-v1-includes-dispatch.md) (this gates row 19),
  [0008](0008-goal-consent-and-account-modes.md) (goal consent)

Words used here: the **hub** is the main program (`fleetd`); a **goal** is one
job handed to a harness (Claude Code, Codex, Muse, Cursor, OpenCode); an
**approval** is the hub waiting for the owner before a risky step; `local_trusted`
is the no-sign-in mode where the hub trusts any program on its own machine.

## Context

In `local_trusted` mode the hub listens on loopback with no sign-in, and every
caller counts as the fixed local owner with every permission
([02f](../specs/02f-hub-authentication.md#modes)). The hub machine also runs its
own agent, over loopback ([02](../specs/02-contracts.md),
[02j](../specs/02j-hub-runtime.md#listeners)), so goals run on that same machine.
The Host and Origin checks stop a hostile web page, not a local program: a
non-browser request may omit Origin ([02f](../specs/02f-hub-authentication.md#host-and-origin-checks)).

So a goal on the hub machine that reads a booby-trapped issue (untrusted text
is data, never instructions: [01e](../specs/01e-security-and-privacy.md)) could
call the loopback API and approve its own request: `DecideApproval` for a
`StartGoal` CONFIRM check (`DISPATCH_CONFIRM`), a destructive
`RunHarnessCommand`, or a `KillWindow` on a developer window
([02m](../specs/02m-hub-api-actions-and-audit.md#actions-that-need-an-approval)).
It could also change settings, rebind accounts, or read session logs. The audit
log would record it, but only after it ran: the approval would be meaningless on
the hub machine. Plan item 0.17 (#62) asked whether approvals and settings
changes there need a local secret. This ADR is the owner's answer.

## Options

| Option | Verdict |
|---|---|
| Leave it, audit only | Rejected: the audit records a self-approved goal after it ran. |
| Approvals and settings changes need a local secret | Chosen, below. |
| Never dispatch to the hub machine in `local_trusted` | Rejected as the whole answer: hand-started sessions there (observed goals) face the same trick, and an owner with one machine would get no dispatch at all. Tighter placement stays row 19's call. |
| `local_trusted` watches only; dispatch needs `authenticated` mode | Rejected: it forces every one-machine owner through certificates before a single local goal can start. |

No arbiter ruling was sought: per decision 2 of ADR 0024 no arbiter ruling
stands in for the owner on an owner decision, so the merge of the PR carrying
this ADR is the decision.

## Decision

**Yes.** In `local_trusted` mode, everyday calls still treat any loopback caller
as the owner, but two kinds of calls need the local approval secret as well, and
fail without it, even on loopback:

1. Approving a request: `DecideApproval`.
2. Changing settings: the calls with the `SETTINGS` or `ADMIN` scope (hub
   settings, config import, account and token handling where offered, log
   deletion). Row 19 (#68) writes the exact list into [02d](../specs/02d-hub-authz.md)
   and [02m](../specs/02m-hub-api-actions-and-audit.md), with a denied-path test
   for each.

The secret and how the owner holds it:

3. The secret is 256-bit random, created at `fleetd init` beside the state
   database (mode 0600), never logged, never in `fleet.yaml` or `ExportConfig`.
   It is shown once at the terminal, terminal only, like the claim URL
   ([02f](../specs/02f-hub-authentication.md#first-run-the-claim-url)), for the
   owner to enter into the dashboard. The dashboard keeps it in the login
   session, never in the shell environment and never in a plain file.
4. Prompt answers from Triage (one answer in flight, typed only while that
   prompt shows: [03d](../specs/03d-triage-sessions-logs.md)) travel the same
   session: only the dashboard session holding the secret can answer. Row 19
   wires this up.
5. Stated limit, same class as the known limit in
   [01e](../specs/01e-security-and-privacy.md): the harness and the agent run as
   the same user, so a file handed to a goal is no boundary. That is why the
   secret lives in the dashboard session, not in a file a goal reads and not in
   the shell. A goal that steals the owner's live browser session is outside
   this boundary. Every use of the secret leaves an audit row
   ([02m](../specs/02m-hub-api-actions-and-audit.md)).

Until row 19 ships this, no goal starts, manual or automatic: the rule in
[02f](../specs/02f-hub-authentication.md#modes) stands, and plan row 22 (goal
start) waits on row 19.

## Consequences

- Row 19 builds the check, the denied-path tests, the dashboard session step
  and the CLI path for scripts that approve or change settings.
- One-machine owners keep dispatch: nothing here forces `authenticated` mode.
- This PR updates [02f](../specs/02f-hub-authentication.md#modes) and
  [01e](../specs/01e-security-and-privacy.md) to point here. The exact call list
  and the session details land with row 19, not here.
