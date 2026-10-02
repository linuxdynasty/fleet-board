# Defaults

- Status: Draft for review
- Date: 2026-10-02
- Decisions: [ADR 0022](../adr/0022-drift-audit-decisions.md) (the rows marked
  "decided here")

Every tunable value the specs rely on, in one place. A spec states a rule; this
file states the number. When a spec and this table disagree, fix the spec.

**Kind.** `setting`: the owner changes it through the hub API
(`UpdateSettings`, `UpdateRepo`, `UpdateAccount`, `UpdateRoutine`) and it is
layered or stored as the spec says. `config`: a key in the hub configuration
file ([02j](02j-hub-runtime.md)); needs a restart. `agent`: the agent's local
policy file or built-in value ([02g](02g-agent-security.md),
[03e](03e-settings-and-drift.md)). `fixed`: a constant in code; changing it is a
spec change. "Decided here" marks a value no earlier document gave.

## Machines and the agent connection

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| Heartbeat | 15 | s | [03](03-workflows.md) A5 | fixed |
| Resource sample (B2) | 30 | s | [02i](02i-delivery-and-sequencing.md) | fixed |
| Machine `STALE` (no heartbeat) | 45 | s | 03 | fixed |
| Machine `OFFLINE` | 3 | min | 03 | fixed |
| Reconnect backoff | 1 to 60, full jitter | s | 03 A2 | fixed |
| `last_seen` written to the store | at most 1 per machine per minute, and on connect and disconnect; held in memory per heartbeat | min | 03g Q10, decided here | fixed |
| Agent event ring cap | 1 week or 64 MiB, whichever first; coalesced kinds evicted before never-dropped ones, every eviction counted (`events_evicted`) | week, MiB | 03, decided here | fixed |
| Agent command log | last 10,000 or 7 days | count, days | 03 | fixed |
| Certificate lifetime | 90 (max 365), renewed at two thirds | days | ADR 0004 | `cert_lifetime` setting |
| Enrollment token lifetime | 15 (max 24 h) | min | machine.proto | per token |
| `max_active` when no layer sets it | 2 | goals | 03e, decided here | `max_active` setting |
| `max_total` when no layer sets it | 4 | goals | 03e, decided here | `max_total` setting |
| `max_windows` when no layer sets it | 12 | windows | 03e, decided here | `max_windows` setting |
| `idle_threshold` | 2 | min | [01](01-agent-capabilities.md) E11 | setting |
| `window_name_template` | `{harness} {work}` | | settings.proto | setting |
| `attach_idle_timeout` | 30 | min | [03d](03d-triage-sessions-logs.md), decided here | setting |
| `spool_cap_bytes` | 2 | GiB | 03e, decided here | setting |
| `rate_limits` (agent, per command kind) | 60 | per min | 03e, decided here | setting |
| `block_on_battery` | false | | 03e | setting |
| `share_identity_email` | false | | 01 C2 | setting |
| `merged_grace` | 1 | hour | [03c](03c-goals.md), decided here | setting (hub-only) |
| Command deadline `not_after_time` | 2 min (`SendKeys`, `Interrupt`, `RunHarnessCommand`, `CapturePane`, `OpenAttach`); 30 min (`StartGoal`, worktree and hook commands); 24 h (`StopProbe`); 10 min else | | [02](02-contracts.md) | fixed |
| `ready_timeout` in `StartGoal` | 90 | s | 03c, decided here | `timing.ready_timeout` config |
| `goal_file_timeout` in `StartGoal` | 30 | s | 03c, decided here | `timing.goal_file_timeout` config |
| Log chunk flush; frame close | 10 s or 256 KiB; 5 min or 4 MiB raw | | 03d, [04](04-technology-review.md) Q6 | fixed |
| `agent.log` rotation | 10 MiB, 3 files | | [05b](05b-observability.md) | fixed |

## Usage, pacing and probes

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| Probe interval | 10 | min | [03b](03b-usage-and-sync.md) | `probe_interval` setting, per account override |
| `stale_after` (a reading older is `STALE` and the account stops dispatching) | 3 intervals, 30 min | | 03b | fixed |
| Passive reading differs from the last probe by | 5 points triggers `run_now` (at most 1 per account per minute) | points | 03b | fixed |
| Probe failover | machine `OFFLINE` 3 min, or 3 consecutive failures | | 03b | fixed |
| `Account.spec.reserve_percent` for a new account | 10 | percent | [03b](03b-usage-and-sync.md), decided here | `UpdateAccount` |
| Account `consent` and `HubSettings.default_consent` | push, pr-open, merge | | ADR 0008 | setting |
| Pacing line adjustment | ahead over 5 points: one fewer; over 10: none; behind by over 5: one more | points | 03b | fixed |
| `base_cap` (concurrent goals per account) | 2 for every harness until learned | goals | 03b, decided here | fixed |
| Last hours before the weekly reset in which the line is ignored | 24 | h | 03b | fixed |
| Short-window speed limit | 95 | percent | 03b | fixed |
| `auto_pause_at_reserve` | false | | 03b | `HubSettings` |
| Insights: spare quota worth a report run | 5 points unused | points | [05](05-insights.md) | fixed |

## Dispatch, goals and routines

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| `HubSettings.auto_dispatch` | false (the owner turns it on; manual dispatch always works) | | 03c, decided here | `HubSettings`, per-repo override |
| Scheduler pass | 30 s tick plus wakeups; at most 1 dispatch per account and per machine per pass | s | [03f](03f-hub-internals.md), 03c | fixed |
| `max_parallel_goals_per_repo` | 3 | goals | 03c | `HubSettings`, per-repo override |
| `max_sync_lag` | 10 | min | 03b | `timing.max_sync_lag` config |
| `verify_grace` | 10 | min | 03c | `timing.verify_grace` config |
| Review-loop cap | 3 rounds beyond the baseline | rounds | 03c | fixed |
| No-progress cooldown | after 2 runs without progress: min(30 min, 2 min x 2^(n-2)) | | 03c | fixed |
| `human_at_keyboard` window | 10 | min | 03c | fixed |
| Start not delivered in time | 30 | min | 03c | fixed |
| Skip a machine after a passing start failure | 5 to 30 | min | [03h](03h-goal-phases-in-depth.md) | fixed |
| Routine `min_interval` (`queue_empty`) | 1 | hour | routine.proto | per routine |
| Routine `blocked_after` | 12 (built-in: 4) | hours | [03i](03i-routines.md) | per routine |
| Built-in weekly report: cron; enabled | `0 7 * * 1` hub time; true | | 05, ADR 0022 | per routine |
| Report retention | 1 year | | 05 | fixed |
| `fresh_context_on_reuse` on a pane mark | false | | [02h](02h-goal-binding-and-pane-signals.md) | per mark |

## Hub: sync, triage, sign-in, storage

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| Work item poll; change request poll | 60 s per repo; 60 s | s | 03b | fixed |
| Claim re-check of a candidate item | 5 | min | 03b | fixed |
| Full poll without validators | 30 | min | 03b | fixed |
| Trusted label | `agent-ready` | | [03j](03j-provider-sync-and-trust.md) | `HubSettings.trusted_gate` |
| `triage_expiry` (`TriageItem.expire_time`) | 7 | days | 03d | `HubSettings` |
| `approval_timeout` (`Approval.expire_time`; on expiry the fallback `DENY` applies) | 30 | min | 03d, decided here | `HubSettings` |
| Approval and triage item are different clocks | an expired approval does not close its item | | 03d | |
| `HubSettings.default_consent` | push, pr-open, merge | | ADR 0008 | `HubSettings` |
| `suggestions_in_triage` | false | | 05 | `HubSettings` |
| `session_idle_timeout`; `session_max_age` | 7 days; 30 days | | 02f | `HubSettings` (ADMIN) |
| Invite lifetime; API token lifetime | 24 h (max 7 days); 90 days (max 365) | | 02f | per call |
| `log_size_cap_bytes`; `log_retention` | 50 GiB; none | | ADR 0010, 0015 | `HubSettings` (ADMIN) |
| Writer batch | at most 100 messages or 250 ms | | ADR 0014 | fixed |
| Read pool | 10 connections | | ADR 0014 | fixed |
| Idempotency record | 24 | h | [02b](02b-hub-api.md) | fixed |
| Watch change log | at least 10 min or 100,000 changes | | [02c](02c-hub-watch-and-filters.md) | fixed |
| `page_size` | 50 (max 500) | rows | 02b | per call |
| Rate limits (hub, per principal) | read 600, write 120, per peer without credentials 30, `/metrics` 60 | per min | 02b, decided here | `rate_limits.*` config |
| Backup | nightly `VACUUM INTO` | | ADR 0014 | fixed |
| System signal thresholds | the table in [05b](05b-observability.md) section 8 | | 05b | fixed |
| Telemetry OTLP | off; `grpc` when on | | 05b | `telemetry.*` config |
