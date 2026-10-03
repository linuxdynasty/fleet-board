# 02k. What each release serves: RPCs, Watch topics, the three programs' commands

- Status: Accepted; contracts unfrozen until v0.1 (ADR 0019)
- Date: 2026-10-02
- Part of: [02b](02b-hub-api.md), [02j](02j-hub-runtime.md)
- Inputs: [`service.proto`](../../api/fleet/hub/v1/service.proto), [`watch.proto`](../../api/fleet/hub/v1/watch.proto),
  [01](01-agent-capabilities.md) (the v1, v2, v3 tags), the [v1 plan](../plan/v1-implementation-plan.md)
- Authority: the protos define each message; this file says which release serves
  it and which plan PR builds it. When it disagrees with a proto, the proto wins.

The hub API has about 100 RPCs and 18 Watch topics, and the plan builds them in
three releases. This file is the one list, so a PR that adds an RPC can say where
it belongs and a reader can tell "not built yet" from "forgotten".

## Out-of-release behaviour

- An RPC the running release does not serve answers gRPC `UNIMPLEMENTED`
  (Connect `unimplemented`). The hub registers every handler of the service so a
  client built from a newer proto gets this answer, not a connection error.
- A `Watch` topic the release does not serve is refused as a whole subscription
  with `INVALID_ARGUMENT` and the topic named. It is never silently empty.
- `GetServerInfo.features` lists exactly what is served (`attach`, `routines`,
  `devices`, `watch.pane_summaries`, and so on). Clients hide what is not listed.
- Sign-in and device RPCs in `local_trusted` stay `FAILED_PRECONDITION`
  ([02f](02f-hub-authentication.md#modes)); that is a mode rule, not a release rule.

## Hub RPCs by release

`PR` is the plan row that builds the handler. Reads of a resource need its store
and sync to exist first (PR 5, 10, 11); the handler itself is the PR named.

| RPCs | Release | PR |
|---|---|---|
| `GetServerInfo`, `GetFleetSummary`, `Watch`, `GetOperation` | v1 | 12 |
| `ListMachines`, `GetMachine`, `ListSessions`, `GetSession`, `CapturePane` | v1 | 12 |
| `ListGoals`, `GetGoal`, `ListGoalRuns`, `ListGoalTransitions` (observed goals) | v1 | 12 |
| `ListWorkItems`, `GetWorkItem`, `ListChangeRequests`, `GetChangeRequest`, `ListIntegrations` | v1 | 12 |
| `ListRepos`, `GetRepo`, `ListAccounts`, `GetAccount`, `ListUsageReadings` | v1 | 12 |
| `ListTriageItems`, `GetTriageItem`, `GetSettings`, `ListSettings`, `ListAuditEntries`, `GetStoreStatus` | v1 | 12 |
| `GetSystemStatus`, `GetInsights` (sections `EFFICIENCY` and `SERIES` only) | v1 | 13 |
| `CreateRepo`, `UpdateRepo`, `DeleteRepo`, `CreateAccount`, `UpdateAccount`, `DeleteAccount`, `UpdateIntegration`, `UpdateSettings` | v1 | 13a |
| `ExportConfig`, `ImportConfig`, `SnoozeTriageItem`, `DismissTriageItem` | v1 | 13a |
| `CreateEnrollmentToken`, `UpdateMachine` (rename, labels), `RevokeMachine`, `DeleteMachine` | v1, **owner to confirm** (addendum to [ADR 0022](../adr/0022-drift-audit-decisions.md), question 1) | 13a |
| `DrainMachine`, `UndrainMachine`, `EnsureMachineHooks` | v2 | none yet |
| `CreateSession`, `SendKeys`, `InterruptPane`, `RunHarnessCommand`, `SetPaneAvailability`, `RespawnPane`, `KillWindow` | v2 | none yet |
| `StartGoal`, `PreflightGoal`, `ControlGoal`, `RequeueWorkItem`, `ProbeAccount` | v2 | none yet |
| `ListApprovals`, `GetApproval`, `DecideApproval` | v2 | none yet |
| `List/Get/Create/Update/DeleteRoutine`, `RunRoutine` | v2 | none yet |
| `ListInsightReports`, `GetInsightReport`, `ListSuggestions`, `DismissSuggestion`, `GetInsights` (other sections) | v2 | none yet |
| `SetLogCapture`, `ReadSessionLog`, `ListStoredSessions`, `GetStoredSession`, `DeleteSessionLogs`, `SearchSessionLogs`, `GetLogStoreStatus`, `CreateAttachTicket`, `AttachTerminal` | v2 (logs and attach) | none yet |
| every RPC of `auth.proto` (sign-in, users, invites, passkeys, login sessions) | v3 | none yet |
| `device.proto` (pairing, devices, notification targets) | v3 | none yet |
| `CreateApiToken`, `ListApiTokens`, `RevokeApiToken` | v3 (they work for the local owner in `local_trusted`, 02f, but no v1 PR builds them) | none yet |

**Watch topics.** v1: `MACHINES`, `SESSIONS`, `PANE_SUMMARIES`, `GOALS`,
`WORK_ITEMS`, `CHANGE_REQUESTS`, `ACCOUNTS`, `TRIAGE`, `SETTINGS`, `OPERATIONS`,
`INTEGRATIONS`, `FLEET_SUMMARY`, `AUDIT`, `REPOS`. v2: `APPROVALS`, `ROUTINES`.
v3: `DEVICES`, `NOTIFICATION_TARGETS`.

## Commands of the three programs

Exit codes are 0 for success and 1 for any failure (design.md). `--json` prints
the Connect JSON of the response message unchanged: field names as protobuf
writes them in JSON, enum names in full, 64-bit integers as decimal strings
([02b](02b-hub-api.md#64-bit-integers)). `fleet status` prints one object,
`{"summary": <GetFleetSummary>, "system": <GetSystemStatus>}`. The golden files
of plan PR 15 fix these shapes. `FLEET_URL` names the hub; `FLEET_TOKEN` is
optional in `local_trusted` (a valid token only narrows the owner's scopes, 02f).

| Command | Does | RPC or local action | Release (PR) |
|---|---|---|---|
| `fleet status` | fleet summary and System health | `GetFleetSummary`, `GetSystemStatus` | v1 (15) |
| `fleet machines`, `accounts`, `goals`, `work` | list and show | the `List*`/`Get*` RPCs | v1 (15) |
| `fleet repo`, `fleet account`, `fleet settings` | the owner-configuration writes | `CreateRepo`..., `UpdateAccount`..., `UpdateSettings` | v1 (15) |
| `fleet export`, `fleet import [--dry-run]` | `fleet.yaml` out and in | `ExportConfig`, `ImportConfig` | v1 (15) |
| `fleet doctor` | reads System status, store status and server info | `GetSystemStatus`, `GetStoreStatus`, `GetServerInfo` | v1 (15) |
| `fleet dispatch`, goal control, `requeue`, routines | the v2 write commands | the v2 RPCs above | v2 |
| `fleet logs export` | stored session log to a file | `ReadSessionLog` | v2 |
| `fleet watch`, `fleet debug stream` | tail or decode a `Watch` | `Watch` | not scheduled |
| `fleetd run`, `init`, `enroll-token` | start, create config and state, mint a token | local, `admin.sock` | v1 (2b) |
| `fleetd backup [--encrypt]`, `restore` | local, never RPCs | local | v1 (3, 5) |
| `fleetd setup github` | the GitHub App manifest and keys | local | v1 (13a) |
| `fleetd insights rebuild` | recompute rollups | local | v1 (13) |
| `fleetd debug agent-stream` | tap the agent bus | local | not scheduled |
| `fleetd claim`, `token create`, `user invite` | sign-in admin | `admin.sock` | v3 |
| `fleet-agent enroll`, `run` | join the fleet, run the agent | `Enroll`, `Connect` | v1 (4) |
| `fleet-agent doctor` | every local check with a reason | local | v1 (6) |
| `fleet-agent hook`, `hooks uninstall`, `drain`, `undrain`, `approve`, `deny` | goal hooks, local drain, local approval | local | v2 |

`fleet backup` in ADR 0002 means `fleetd backup`. `fleet` runs on any machine
that can reach the API: the hub's machine until v3 (`local_trusted` binds
loopback), then any machine (`authenticated`).

## Labels that change behaviour

A label is free text the owner or the local policy adds ([03e](03e-settings-and-drift.md)).
Only these two are read by the hub:

| Label | Effect | Where |
|---|---|---|
| `always-on` | first choice for a usage probe; its absence for over 10 minutes raises the `AGENT_CONNECTED` System item | [03b](03b-usage-and-sync.md), [05b](05b-observability.md) |
| `laptop` | ranked below a machine without it when placing a probe | 03b |

## The test seed

PRs 12 to 15 test against one **seed package**, built in PR 12: it replays a
recorded agent stream (captured from the PR 5 fake agent) into a real hub store
under `testdata/`, and is versioned with the protos. The Playwright smoke per
view, the golden CLI output and the contract tests all start from it.
