# Defaults

- Status: Accepted
- Date: 2026-10-02
- Decisions: [ADR 0022](../adr/0022-drift-audit-decisions.md) (the rows marked
  "decided here"), [ADR 0024](../adr/0024-pre-build-owner-decisions.md)
  (the rows marked "ADR 0024"), [ADR 0025](../adr/0025-v1-includes-dispatch.md)
  (dispatch is part of v1)

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
| Heartbeat | 15 (the agent follows `HelloAck.heartbeat_interval` from 1 to 15 s) | s | [03](03-workflows.md) A5 | fixed |
| Resource sample (B2) | 30 | s | [02i](02i-delivery-and-sequencing.md) | fixed |
| Machine `STALE` (no heartbeat) | 45 | s | 03 | fixed |
| Machine `OFFLINE` | 3 | min | 03 | fixed |
| Reconnect backoff | 1 to 60, full jitter (after n failed tries in a row, uniform from 1 to 2^n s, capped at 60 s, so the first wait is 1 to 2 s; a connect, a wake or a network change resets n); at least 5 s between connects (agent), one `Hello` per machine per 5 s (hub) | s | 03 A2, [03](03-workflows.md) failure table | fixed |
| Enroll tries (agent) | 4 calls per `fleet-agent enroll`; between them a full-jitter wait up to 0.5, 1, 2 s (doubling, cap 4 s); 20 s per call, 60 s for the whole enroll | s | [03](03-workflows.md) W1 failure table, decided here | fixed |
| mDNS re-query (agent); mDNS browse timeout | no interval of its own: one browse per discovery attempt, on the reconnect backoff above; 2 s per browse, asked as a 0.5 s query then a 1.5 s one (an answer is read when its query returns) | s | 03 A2, [ADR 0028](../adr/0028-mdns-best-effort.md), [ADR 0030](../adr/0030-mdns-browse-as-short-queries.md), spike 0.7 ([results](../../spikes/mdns/results/README.md)) | fixed |
| mDNS answer budget; mDNS phase budget (agent) | 3 for one answer's dial and TLS handshake together; 8 for the mDNS part of one attempt or one enroll search, then the configured addresses | s | 03 A2, [ADR 0030](../adr/0030-mdns-browse-as-short-queries.md) | fixed |
| Random delay before the attempt a wake or network change starts | 20 to 120 | ms | 03 A2, ADR 0028 | fixed |
| Wake check | every 5 s; a wake is the time asleep since boot growing by 2 s or more since the last check (Linux `CLOCK_BOOTTIME` minus `CLOCK_MONOTONIC`, macOS `CLOCK_MONOTONIC_RAW` minus `CLOCK_UPTIME_RAW`), or the wall clock moving 2 s or more past Go's monotonic clock; a wall clock stepped back 2 s or more is logged, not a wake | s | 01 A6, [ADR 0029](../adr/0029-wake-and-network-change-detection.md), spike 0.6 | fixed |
| Network-change confirmation | a routing push message is only a hint: after 500 ms with no further hint (at most 2 s after the first), the interface, address and default-route list is read and compared with the last; no difference, no action | ms | 01 A6, ADR 0029, decided here (observed bursts lasted under 10 ms) | fixed |
| Network-change poll when the push socket is unavailable | 15 | s | 01 A6, ADR 0029 | fixed |
| Network-change read retry (push socket on) | 2 s after a failed read, then every 15 s, until a read works or a hint arrives | s | 01 A6, ADR 0029, decided here | fixed |
| Transport keepalive (agent) | ping every 10 s, 20 s timeout; the hub's keepalive enforcement allows it | s | ADR 0004 | fixed |
| HelloAck wait (agent) | 30; no `HelloAck` (or `Bye`) by then closes the stream and backs off. Keepalive pings do not cover a hub that answers pings but not `Hello` | s | [03](03-workflows.md) W1, decided here | fixed |
| TLS handshake per hub address (agent) | 10; a handshake not done by then is a lost connection: retried within "Enroll tries" when enrolling, the next candidate in the connection loop | s | [03](03-workflows.md) W1, decided here | fixed |
| Bye wait on stop (agent) | 2; after `Bye{SHUTDOWN}` the agent waits this long for the hub to close the stream, then cuts the connection (`connect.shutdown_cut`), so a hub that stopped reading cannot hold up the stop | s | 01 A7, decided here | fixed |
| `last_seen` written to the store | at most 1 per machine per minute, and on connect and disconnect; held in memory per heartbeat | min | 04, decided here | fixed |
| Agent event ring cap | 1 week or 64 MiB, whichever first; the oldest entry is evicted first, whatever its class, and every eviction is counted (`EventCounters.evicted_total`) | week, MiB | [02i](02i-delivery-and-sequencing.md), decided here | fixed |
| Agent command log | last 10,000 or 7 days; neither limit removes an entry that is not acked and inside its deadline (02i) | count, days | 03 | fixed |
| Agent status file (`status.json`) rewrite | at most once per second (changes in between go into the next write), at least once a minute, and once more before the agent exits | s, min | [01f](01f-agent-control-goals-and-policy.md) M3, plan row 6 | fixed |
| One-shot child process (`internal/proc`) | time cap and output cap per named caller (`loginctl`, `tmux` one-shot, version check, `git`, `gh`), each set from a measured run of that command. The output cap counts stdout and stderr together. None is measured yet, so the table in `internal/proc/limits.go` is empty and no caller has a default; the row that adds a caller measures its real call and writes its caps here as `name` seconds / KiB and in `limits.go`, in the same PR. After a kill, or when a child has exited, the helper waits at most 2 s (`WaitDelay`) for the output pipes | s, KiB | 01 principle 8, plan row 2c | fixed |
| Agent command rate limit per command kind (`rate_limits`) | 60 | per min | 03e, decided here | `agent` |
| Certificate lifetime | 90 (max 365), renewed at two thirds | days | ADR 0004 | `cert_lifetime` setting |
| Agent certificate renewal check | after each `HelloAck` and hourly while connected; a failure retried at the next check; the first check of a stream runs once it is connected, after its snapshot, and no renewal starts within an hour of the last. Per the 02g amendment: (a) a handshake presents the valid pair that ends later by the agent's clock; (b) the two-thirds point, the expiry check and the expiry gauge follow the pair that ends last; (c) the two-thirds point is judged on hub time (`HelloAck.hub_time` plus the time since, counting sleep); (d) so are the expiry check and gauge; (e) after an attempt fails before Connected the next presents the other pair while it is valid | h | 02g | fixed |
| Hub server certificate lifetime (agent listener) | 90, re-issued at two thirds (checked at start and hourly) or when `tls.agent_names` changes; never past the CA. Its names always include `fleetd-<hostname>.local` (02j Discovery) | days | [02g](02g-agent-security.md#hub-identity-and-restore), plan row 2b | fixed |
| Fleet CA lifetime | 10; no rotation in v1 | years | 02g, plan row 2b | fixed |
| Enrollment token lifetime | 15 (max 24 h) | min | machine.proto | per token |
| `default_server` (local policy; hub settings can only tighten) | `control`; the local policy can lower it to `observe` or `none` | | ADR 0011, settings.proto | `agent`; also a `SettingValues` field |
| `max_active` when no layer sets it | 2 | goals | 03e, decided here | `max_active` setting |
| `max_total` when no layer sets it | 4 | goals | 03e, decided here | `max_total` setting |
| `max_windows` when no layer sets it | 12 | windows | 03e, decided here | `max_windows` setting |
| Allowed range of `max_active`; of `max_total` (a value outside it is refused; in `policy.yaml` it is safe mode) | 1 to 16; 1 to 32 | goals | ADR 0024 | `agent`, `setting` |
| `allowed_repo_roots` of a fresh agent; `allowed_harnesses` of a fresh agent | empty until the owner sets it (`fleet-agent enroll` asks); every installed harness | | ADR 0024, 03e | `agent` |
| `schema_version` (`policy.yaml`) | `"1"`; an unknown value is safe mode | | [03e](03e-settings-and-drift.md) | `agent` |
| `local_approval` (`policy.yaml`) | empty: off for every command kind | | 03e, [02g](02g-agent-security.md) | `agent`; also a `SettingValues` field |
| `min_probe_interval` (`policy.yaml`; a floor the hub's probe interval cannot go below) | none; at least 1 min when set | min | 03e, [03b](03b-usage-and-sync.md) | `agent` |
| `labels` (`policy.yaml`) | empty | | 03e | `agent`; also a `SettingValues` field |
| `metrics_listen`; `pprof_listen` | none: the agent has no listener | | 03e, [05b](05b-observability.md) | `agent`, local only |
| `telemetry` (`policy.yaml`) | off | | 03e, 05b | `agent`, local only |
| Typing detection: tmux `session_activity` subscription (tmux re-checks it about once a second) plus one `list-clients` read per change; the 2 s `list-clients` poll runs only while the subscription is not confirmed | about 1 (set by tmux); backup 2 | s | [02h](02h-goal-binding-and-pane-signals.md), ADR 0022 A.6; backup: ADR 0024 decision 9 | fixed |
| Goal-file rescan for missed file events | 60 | s | [02h](02h-goal-binding-and-pane-signals.md), ADR 0024 (the other allowed exception) | fixed |
| Agent low-disk point (`DISK_LOW` under any repo root) | 5 | GiB free | ADR 0024 | fixed |
| `idle_threshold` | 2 | min | [01](01-agent-capabilities.md) E11 | setting |
| `window_name_template` | `{harness} {work}` | | settings.proto | setting |
| `attach_idle_timeout` | 30 | min | [03d](03d-triage-sessions-logs.md), decided here | setting |
| `spool_cap_bytes` | 2 | GiB | settings.proto, decided here | setting |
| `rate_limits` (agent, per command kind) | 60 | per min | decided here | setting |
| `block_on_battery` | false | | 03e | setting |
| `share_identity_email` | false | | 01 C2 | setting |
| `merged_grace` | 1 | hour | [03c](03c-goals.md), decided here | setting (hub-only) |
| Command deadline `not_after_time` | 2 min (`SendKeys`, `Interrupt`, `RunHarnessCommand`, `CapturePane`, `OpenAttach`); 30 min (`StartGoal`, worktree and hook commands); 24 h (`StopProbe`); 10 min else | | [02](02-contracts.md) | fixed |
| `ready_timeout` in `StartGoal` | 90 | s | [02j](02j-hub-runtime.md), decided here | `timing.ready_timeout` config |
| `goal_file_timeout` in `StartGoal` | 30 | s | 02j, decided here | `timing.goal_file_timeout` config |
| Log writer batch | at most 1 | s | [03f](03f-hub-internals.md) | fixed |
| Clock skew: warning; machine data ordered by hub receive time | 5; 15 | min | machine.proto | fixed |
| Log chunk flush; frame close | 10 s or 256 KiB; 5 min or 4 MiB raw | | 03d, [04](04-technology-review.md) Q6 | fixed |
| `agent.log` rotation | 10 MiB, 3 files | | [05b](05b-observability.md) | fixed |

## Usage, pacing and probes

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| Probe interval | 10 | min | [03b](03b-usage-and-sync.md) | `probe_interval` setting, per account override |
| `stale_after` (a reading older is `STALE` and the account stops dispatching) | 3 intervals, 30 min | | 03b | fixed |
| Passive reading differs from the last probe by | 5 points triggers `run_now` (at most 1 per account per minute) | points | 03b | fixed |
| Probe failover | machine `OFFLINE` 3 min, or 3 consecutive failures | | 03b | fixed |
| `Account.spec.reserve_percent` for a new account; its allowed range | 10; 0 to 50 | percent | [03b](03b-usage-and-sync.md), decided here; range ADR 0024 | `UpdateAccount` |
| Waste projection (`projected_unused_percent`) is shown only after | the first 24 hours of each usage window | h | 03b, ADR 0024 | fixed |
| Account `consent` (a new account starts with it) and `HubSettings.default_consent` | push, pr-open, merge | | ADR 0008 | setting |
| Pacing line adjustment | ahead over 5 points: one fewer; over 10: none; behind by over 5: one more | points | 03b | fixed |
| `base_cap` (concurrent goals per account) | 2, the same for every harness; nothing is learned | goals | 03b, decided here | fixed |
| Last hours before the reset of a weekly or monthly bucket in which the line is ignored | 24 | h | 03b | fixed |
| A monthly window the hub saw between two resets is used only when it is | 28 d to 31 d 1 h | d | 03b, "Monthly windows" | fixed |
| A reset shown as a date with no time ("Resets Nov 4") means | the end of that date (00:00 the next day), machine timezone | | 03b, "Monthly windows"; 01 D2 | fixed |
| Short-window speed limit | 95 | percent | 03b | fixed |
| `auto_pause_at_reserve` | false | | 03b | `HubSettings` |
| Insights: spare quota worth a report run | 5 points unused | points | [05](05-insights.md) | fixed |

## Dispatch, goals and routines

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| `HubSettings.prefer_non_hub` | true (a new window goes to a machine other than the hub's first, then the hub machine) | | 03c, decided here | `HubSettings` |
| `HubSettings.auto_dispatch` | false (the owner turns it on once owner decision 20 of ADR 0024 is made, and for a repo without eshu's hooks once decision 21 is made too: ADR 0025; manual dispatch works once the dispatch rows exist, and they wait on decision 20) | | 03c, decided here | `HubSettings`, per-repo override |
| Scheduler pass | 30 s tick plus wakeups; at most 1 dispatch per account and per machine per pass | s | [03f](03f-hub-internals.md), 03c | fixed |
| `max_parallel_goals_per_repo` | 3 | goals | 03c | `HubSettings`, per-repo override |
| `max_sync_lag` | 10 | min | 03m | `timing.max_sync_lag` config |
| `verify_grace` | 10 | min | 03c | `timing.verify_grace` config |
| Review-loop cap | 3 rounds beyond the baseline | rounds | 03c | fixed |
| No-progress cooldown | after 2 runs without progress: min(30 min, 2 min x 2^(n-2)) | | 03c | fixed |
| `human_at_keyboard_until_time` window (after the latest input) | 10 | min | 03c | fixed |
| Start not delivered in time | 30 | min | 03c | fixed |
| `PANE_LOST` becomes `STUCK` | 10 | min | 03c | fixed |
| Skip a machine after a passing start failure | 5 to 30; after `WORKTREE_EXISTS` 1 h (that machine and repo) | min, h | [03h](03h-goal-phases-in-depth.md) | fixed |
| Routine `min_interval` (`queue_empty`) | 1 | hour | routine.proto | per routine |
| Routine `blocked_after` | 12 (built-in: 4) | hours | [03i](03i-routines.md) | per routine |
| Built-in weekly report: cron; enabled | `0 7 * * 1` hub time; true | | 05, ADR 0022 | per routine |
| Report retention | 1 year | | 05 | fixed |
| `fresh_context_on_reuse` on a pane mark | false | | [02h](02h-goal-binding-and-pane-signals.md) | per mark |

## Hub: sync, triage, sign-in, storage

| Name | Value | Unit | Owner | Kind |
|---|---|---|---|---|
| Work item poll; change request poll | 60 s per repo; 60 s | s | 03m | fixed |
| Claim re-check of a candidate item | 5 | min | 03m | fixed |
| Full poll without validators | 30 | min | 03m | fixed |
| Trusted label | `agent-ready` | | [03j](03j-provider-sync-and-trust.md) | `HubSettings.trusted_gate` |
| `triage_expiry` (`TriageItem.expire_time`) | 7 | days | 03d | `HubSettings` |
| `limit_wait` triage item raised when the wait is expected over | 30 | min | 03d | fixed |
| `approval_timeout` (`Approval.expire_time`; on expiry the fallback `DENY` applies) | 30 | min | 03d, decided here | `HubSettings` |
| Approval and triage item are different clocks | an expired approval does not close its item | | 03d | |
| `HubSettings.default_consent` | push, pr-open, merge | | ADR 0008 | `HubSettings` |
| `suggestions_in_triage` | false | | 05 | `HubSettings` |
| `session_idle_timeout`; `session_max_age` | 7 days; 30 days | | 02f | `HubSettings` (ADMIN) |
| Invite lifetime; API token lifetime | 24 h (max 7 days); 90 days (max 365) | | 02f | per call |
| Pairing code; attach ticket; sign-in ceremony; registration session | 10 min; 60 s; 5 min; 15 min | | 02d, 02f | fixed |
| `passkey_sign_in`, `github_sign_in` | on once the owner has registered that credential type | | settings.proto | `HubSettings` (ADMIN) |
| `log_size_cap_bytes`; `log_retention` | 50 GiB; none | | ADR 0010, 0015; counted as live pages of `logs.db`, [04](04-technology-review.md) Q6 | `HubSettings` (ADMIN) |
| Writer batch | at most 100 messages or 250 ms after the batch's first message | | ADR 0014 | fixed |
| Writer queue (jobs waiting for the writer; submitting waits when it is full) | 1,000 (ten full batches) | jobs | [03f](03f-hub-internals.md), plan row 2 | fixed |
| Read pool | 10 connections | | ADR 0014 | fixed |
| Watch `CHECKPOINT` interval on an idle stream (the checkpoint interval) | 30 | s | [02c](02c-hub-watch-and-filters.md) | fixed |
| `FLEET_SUMMARY` Watch message (the summary interval) | at most 1 | per s | 02c | fixed |
| System evaluation interval (the hub judges the signals) | 15 | s | [05b](05b-observability.md) | fixed |
| System status poll (the dashboard calls `GetSystemStatus`) | 30 | s | 05b | fixed |
| OAuth state cookie lifetime (`__Host-fleet_oauth`, and its server-side record) | 10 | min | [02f](02f-hub-authentication.md) | fixed |
| Attach output limit (one `AttachTerminalResponse`) | 64 | KiB | 02f, ADR 0016 | fixed |
| Idempotency record | 24 | h | [02b](02b-hub-api.md) | fixed |
| Watch change log | at least 10 min or 100,000 changes | | [02c](02c-hub-watch-and-filters.md) | fixed |
| `page_size` | 50 (max 500) | rows | 02b | per call |
| Rate limits (hub, per principal) | read 600, write 120, per peer without credentials 30, `/metrics` 60 | per min | 02b, decided here | `rate_limits.*` config |
| Backup | nightly `VACUUM INTO` at 03:00 hub local time; keep the last 7 nightly copies (manual copies are never pruned); checked every 1 min; catch-up at start or wake when the newest nightly copy is older than 24 h or there is none; crashed backups' temporary files removed at start once older than 1 h; a stamp more than 10 min past the clock does not count as the newest | | ADR 0014; hour and count ADR 0024; check and catch-up [02j](02j-hub-runtime.md#state-directory) | fixed |
| Log shipping pauses below; resumes above (free disk on the hub's `logs.db` volume) | 5; 8 | GiB | [03d](03d-triage-sessions-logs.md), ADR 0024 | fixed |
| System signal levels, holds and hysteresis | the table and the Hysteresis paragraph in [05b](05b-observability.md) section 8 | | 05b | fixed |
| Telemetry OTLP | off; `grpc` when on | | 05b | `telemetry.*` config |

## Runtime, install and access in v1

The rules (staying awake, logout, install, upgrade, API-key spend) live in
[02j](02j-hub-runtime.md) and [03b](03b-usage-and-sync.md). GitHub access is the
GitHub App only: [ADR 0024](../adr/0024-pre-build-owner-decisions.md) decision
15. The numbers:

| Name | Value | Owner | Kind |
|---|---|---|---|
| Dashboard and API port in `local_trusted` mode (same port on both sides of the `ssh -L` tunnel) | 7080 | [02j](02j-hub-runtime.md#listeners) | `config` |
| Agent listener port | 7443 | 02j | `config` |
