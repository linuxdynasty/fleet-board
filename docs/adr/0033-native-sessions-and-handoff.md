# 0033. Qualified native sessions and explicit handoff

- Status: Accepted
- Date: 2026-10-18
- Decider: linuxdynasty (headless direction, 2026-10-13)
- Supersedes, in part: [0024](0024-pre-build-owner-decisions.md) decision 1
  (observed goals cannot ever be handed off) and decision 10 (the product
  never runs headless); [0027](0027-cursor-first-class-copilot-and-messaging-later.md)
  decision 1 (Cursor's blanket no-headless rule)
- Builds on: [0008](0008-goal-consent-and-account-modes.md),
  [0025](0025-v1-includes-dispatch.md), [0032](0032-local-trusted-approval-secret.md)

## Context

The owner wants to work in a session by hand, then explicitly give Fleetboard
control. The blanket product ban on headless harness execution prevents a
supported native session path. A terminal pane remains useful, but it is not
the only possible interface. The owner removed that blanket ban on
2026-10-13. The policy must change before the runtime does.

## Options

Keep all sessions in tmux; permit every SDK and `-p` invocation immediately;
or permit each native interface only after its exact version, account mode,
permissions and machine-side caller pass a qualification gate. Choose the
last option. The [capability record](../research/native-session-capabilities.md)
keeps evidence and missing proof visible.

## Decision

1. A supported SDK or structured protocol is the preferred native interface.
   A provider's documented `-p` route is another candidate. Terminal control
   stays an explicit fallback. No candidate runs merely because the binary
   accepts an argument or an API exists. Native execution starts **disabled**.
2. Qualification is per harness version, interface, auth and billing mode,
   deployment, approval behavior and machine-side caller. It needs primary
   provider guidance or provider confirmation for the exact auth and deployment
   combination, plus local behavior proof. Unknown or conflicting permission
   evidence leaves that combination disabled. A product or arbiter decision
   cannot grant provider permission.
3. The owner signs in through each harness's supported local flow. Credentials
   stay with that harness on its worker machine. Fleetboard stores only a
   non-secret account reference. No token, auth file, keychain item, raw
   credential, copied login or hidden API-key fallback enters the hub.
4. A **manual** session belongs to the developer. An **observed-only** goal
   reports state and receives no Fleetboard control commands. A
   **fleet-managed** session has one durable machine-side controller and
   may receive typed commands only within local policy, account mode, goal
   consent and approval limits. Handoff in either direction is an explicit
   operation. A manual session cannot be silently cloned, reclassified or
   driven while its earlier controller still owns it. Failed or uncertain
   handoff keeps the session held for reconciliation.
5. A successful native turn means only that the harness finished that turn.
   The issue ends only after the code-host verifier confirms its agreed
   state. Permission to merge is separate from both. For Fleetboard's own
   v1 PRs, the owner-merges rule from ADR 0024 remains the standing project
   rule. This decision does not widen default goal consent.
6. A managed native session can receive turns through its own qualified
   session interface. This does not enable sending messages through another
   running session's inbox or `codex queue`: that cross-session feature
   remains `later` under ADR 0027 decision 4, with its own checks.
7. Existing deployment stays local: browser to hub, outbound agent stream to
   a worker on an allowed machine, and a local harness there. Cloud workers,
   remote browser access to a worker, root and new sandbox requirements are
   outside this decision. The hub still sends typed, replay-protected commands;
   the agent applies the local policy ceiling and audit rules. Secret text is
   scrubbed before storage or telemetry.

## Consequences

Native and handoff runtime work belongs to #266–#274. In particular, a
non-bare Claude `-p`/SDK start may load project hooks or MCP servers before
Fleetboard sees any prompt. Official `-p` guidance says there is no workspace
trust dialog or per-server approval prompt, even in an untrusted folder. #268
must prove a prelaunch boundary before any such start. Bare `-p` is not a
silent fallback: it skips the
normal OAuth/keychain route and needs a separately qualified API billing path.
The current observed-goal contract remains read-only until #270 adds an
explicit, durable handoff protocol. No existing plan row or issue completion
criterion is changed by this policy decision.
ADR 0024's headless development-helper allowance still applies only to
building this repository. ADR 0027's credential and pane-typing checks remain
in force for Cursor; only its blanket native-execution ban is superseded.
