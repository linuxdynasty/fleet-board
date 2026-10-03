# ADR 0019: index of its numbered decisions

[ADR 0019](0019-pre-implementation-contract-decisions.md) holds 42 numbered
decisions under a title that names four. This page is only a finding aid: one
line per decision, the spec that carries the detail, and the addendum it sits in.
The ADR is the authority; when a later ADR changes a decision, the Relations
column of the [ADR index](README.md) says so.

| # | Decision in one line | Spec | Section of the ADR |
|---|---|---|---|
| 1 | A goal reuses a pane only in a clean checkout of the same repo, on a new branch in place | [02h](../specs/02h-goal-binding-and-pane-signals.md), [03c](../specs/03c-goals.md) | Decision |
| 2 | Sign-in is passkeys, GitHub or both, from a one-time claim URL | [02f](../specs/02f-hub-authentication.md) | Decision |
| 3 | Contracts are unfrozen until `v0.1`, additive-only after | [02e](../specs/02e-contract-rules.md) | Decision |
| 4 | Stored terminal logs need the `LOGS` scope: owner by default, explicit grant otherwise | [02d](../specs/02d-hub-authz.md), [03d](../specs/03d-triage-sessions-logs.md) | Decision |
| 5 | Resources have `spec` (chosen) and `status` (observed) | [02b](../specs/02b-hub-api.md) | Hub API conventions |
| 6 | `Watch` has one filter per topic; a changed subscription resumes the others | [02c](../specs/02c-hub-watch-and-filters.md) | Hub API conventions |
| 7 | Roles map to scopes in one table | [02d](../specs/02d-hub-authz.md) | Hub API conventions |
| 8 | Naming rules: `_time` timestamps, `Duration`, id wrappers | [02e](../specs/02e-contract-rules.md) | Hub API conventions |
| 9 | Reservations dropped; codegen config; 64-bit values are strings in JSON | [02e](../specs/02e-contract-rules.md), [02b](../specs/02b-hub-api.md) | Hub API conventions |
| 10 | One credential kind per transport; Host and Origin checks on every request | [02f](../specs/02f-hub-authentication.md) | Auth, identity, security |
| 11 | People are users, invited once; the claim URL is the first invite | [02f](../specs/02f-hub-authentication.md) | Auth, identity, security |
| 12 | API tokens: `fbt_<id>.<secret>`, shown once, a subset of the creator's scopes | [02f](../specs/02f-hub-authentication.md) | Auth, identity, security |
| 13 | Browser attach over WebSocket needs a 60 s single-use ticket | [02f](../specs/02f-hub-authentication.md) | Auth, identity, security |
| 14 | The agent is told the narrowest role covering the caller's scopes | [02d](../specs/02d-hub-authz.md), [02g](../specs/02g-agent-security.md) | Auth, identity, security |
| 15 | The enrollment token pins the CA hash; `hub_identity` lives in `state.db` | [02g](../specs/02g-agent-security.md) | Auth, identity, security |
| 16 | Revoke keeps the row; delete only follows a revoke | [02g](../specs/02g-agent-security.md) | Auth, identity, security |
| 17 | The goal registry is the only goal identity; `StartGoal` has 14 named steps | [02h](../specs/02h-goal-binding-and-pane-signals.md) | Goal binding and pane signals |
| 18 | A person's interrupt pauses the goal; the owner decides | [02h](../specs/02h-goal-binding-and-pane-signals.md), [03h](../specs/03h-goal-phases-in-depth.md) | Goal binding and pane signals |
| 19 | The hub enforces slots; ids are lowercase ULIDs; `RepoRef` is host and path | [03c](../specs/03c-goals.md), [02e](../specs/02e-contract-rules.md) | Goal binding and pane signals |
| 20 | Counters never go backwards; loss is counted | [02i](../specs/02i-delivery-and-sequencing.md) | Delivery, logs, settings |
| 21 | Logs are keyed by pane; every chunk is acked | [03d](../specs/03d-triage-sessions-logs.md) | Delivery, logs, settings |
| 22 | The hub is the clock authority; refused settings are reported back | [02i](../specs/02i-delivery-and-sequencing.md), [03e](../specs/03e-settings-and-drift.md) | Delivery, logs, settings |
| 23 | Per-repo settings are a `Repo` resource | [02b](../specs/02b-hub-api.md), [03c](../specs/03c-goals.md) | Domain resources, goal lifecycle |
| 24 | Every dispatch attempt is a new goal (`attempt + 1`); "active" is a table | [03c](../specs/03c-goals.md), [03h](../specs/03h-goal-phases-in-depth.md) | Domain resources, goal lifecycle |
| 25 | The provider closing the work under a goal pauses it; the owner decides | [03h](../specs/03h-goal-phases-in-depth.md) | Domain resources, goal lifecycle |
| 26 | "Not done" re-arms the goal; it is not a nudge | [03h](../specs/03h-goal-phases-in-depth.md) | Domain resources, goal lifecycle |
| 27 | Manual dispatch has four check severities: Block, Confirm, Warn, Info | [03h](../specs/03h-goal-phases-in-depth.md) | Domain resources, goal lifecycle |
| 28 | Accounts are discovered; the owner may add API-key accounts | [03b](../specs/03b-usage-and-sync.md) | Domain resources, goal lifecycle |
| 29 | The hub database is the truth; config moves as one plan | [02b](../specs/02b-hub-api.md) | Domain resources, goal lifecycle |
| 30 | Stored logs have a management API under `LOGS` | [03d](../specs/03d-triage-sessions-logs.md) | Domain resources, goal lifecycle |
| 31 | The hub owns the polling loop; adapters are stateless | [03j](../specs/03j-provider-sync-and-trust.md) | Integration contract |
| 32 | The trusted-issues gate is the hub's rule over adapter evidence and fails closed | [03j](../specs/03j-provider-sync-and-trust.md) | Integration contract |
| 33 | Small rules the drift fixes settled (settings generation, probe skips) | [03e](../specs/03e-settings-and-drift.md), [03b](../specs/03b-usage-and-sync.md) | Doc drift |
| 34 | Names in ADR 0018 that no longer exist | this ADR | Doc drift |
| 35 | Small rules the flows settled (`RunRoutine` checks, `ControlGoal` while held) | [03i](../specs/03i-routines.md), [03c](../specs/03c-goals.md) | Gaps the flows exposed |
| 36 | No escalation: a credential or grant is limited to the caller's own scopes | [02d](../specs/02d-hub-authz.md) | Final review defaults |
| 37 | `LOGS` covers stored or scrolled-back terminal history and others' output text | [02d](../specs/02d-hub-authz.md) | Final review defaults |
| 38 | Authenticated mode is HTTPS only | [02f](../specs/02f-hub-authentication.md) | Final review defaults |
| 39 | GitHub sign-in state cookie | [02f](../specs/02f-hub-authentication.md) | Final review defaults |
| 40 | `UserSpec.extra_scopes` (only `LOGS` in v1) | [02d](../specs/02d-hub-authz.md) | Final review defaults |
| 41 | Sign-in, session and log-cap settings need `ADMIN` | [02d](../specs/02d-hub-authz.md) | Final review defaults |
| 42 | Smaller rules the final review settled (allow-list, break-glass, revoke effects, local-trusted rules) | [02f](../specs/02f-hub-authentication.md), [02g](../specs/02g-agent-security.md) | Final review defaults |
