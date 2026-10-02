# 03g. Load table, questions for step 4, contract changes, open questions

- Part of: [03-workflows.md](03-workflows.md)

## Load and volume table for step 4

**Fleet assumptions.** *A4* (the author's fleet, design.md): 4 machines, 3
accounts, 1 repo on 1 integration, 20 observed panes, 10 goal panes capturing
logs (the sum of `max_active` is 2+2+4+2 = 10), 2 browsers. *A50*: 50
machines × 10 panes = 500 observed panes, 3 accounts (01c; accounts scale
with people, not machines), 5 repos, 300 goal panes capturing (`max_total` ≈ 6
per machine), 150 active goals (`max_active` ≈ 3), 10 browsers.

**Measured inputs (01c, 2026-10-01, Muse on a real goal).** Raw terminal
output 26.9 MB/h per busy pane = 26.9 × 10⁶ ÷ 3600 = **7.47 KB/s**; redraw
floor 274 KB/min = **4.57 KB/s** even when quiet; zstd-3 0.23 MB/h = 234 KB ÷
3600 = **65 B/s** per pane; Muse transcript 13 MB/h = **3.6 KB/s** raw.
**Contract rates (02).** Heartbeat every 15 s; summaries ≤ 1 per pane per
5 s; resource sample every 30 s; probes every 10 min per account.
**Modelling assumptions** (stated so step 4 can change them): heartbeat 0.5
KB, summary 2 KB, activity transition 0.3 KB at 2 per minute per active goal
pane, goal state and branch state 2 KB at 6 per hour per goal each, prompt or
condition 2 per hour per active goal, usage reading 3 KB, command or result 1
KB, snapshot 100 KB, log chunk every 10 s per stream.

| # | Workflow / stream | A4 steady | A50 steady | Peak | Durability | Ordering | Browser fan-out |
|---|---|---|---|---|---|---|---|
| 1 | W1/W2 snapshots | ≈ 0 (reconnects rare) | ≈ 0 | hub restart: 4 × 100 KB in ≤ 60 s = 6.7 KB/s; 50 × 100 KB = 5 MB in 60 s = **83 KB/s**, 50 apply-txs | must-persist | Hello → Snapshot → results → events, per agent | none (UI re-reads) |
| 2 | W2 ring replay | ≈ 0 | ≈ 0 | per agent ≤ 200 ev/s × 0.5 KB = 100 KB/s; 50 agents uncapped = 10,000 ev/s = 5 MB/s → **hub-wide budget 2,500 ev/s (1.25 MB/s) for ~1 min** | must-persist (history) | per agent `seq` | none |
| 3 | A5 heartbeats | 4 ÷ 15 = 0.27/s × 0.5 KB = 0.13 KB/s | 50 ÷ 15 = 3.3/s = **1.7 KB/s** | same | can-lose (latest in memory; `last_seen` write optional, 3.3 rows/s at A50) | none | coalesced to the summary strip, 1/s per browser |
| 4 | B2 resource samples | 4 ÷ 30 = 0.13/s = 0.07 KB/s | 50 ÷ 30 = 1.7/s = **0.8 KB/s** | same | can-lose; 1 week = 1.7 × 604,800 = **1.0 M rows/week** | none | Machines view only |
| 5 | E5 pane summaries | 20 × 0.2 = 4/s × 2 KB = **8 KB/s** | 500 × 0.2 = 100/s × 2 KB = **200 KB/s** (720 MB/h) | same | can-lose; **latest per pane only** (persisting 1 week = 100 × 86,400 = 8.6 M rows/day, 17 GB/day; see Q2) | per pane, latest wins | unfiltered: 2 browsers × 8 = 16 KB/s; 10 × 200 = **2 MB/s**; filtered to ~30 visible panes per browser: 10 × 6/s × 2 KB = 120 KB/s |
| 6 | E4 activity transitions | 10 × 2 ÷ 60 = 0.33/s × 0.3 KB = 0.1 KB/s | 150 × 2 ÷ 60 = **5/s**, 1.5 KB/s | provider incident: 150 in 10 s = 15/s; replay bursts (row 2) | must-persist, never dropped | per agent `seq` | Session view, per visible pane |
| 7 | G2/H3 goal + branch state | 10 × 12 ÷ 3600 = 0.03/s, 0.07 KB/s | 300 × 12 ÷ 3600 = **1/s**, 2 KB/s | dispatch wave: 50 goals × 2 in 1 min = 1.7/s | must-persist, never dropped | per agent `seq` | all browsers: 10 × 1/s = 10 msg/s, 20 KB/s |
| 8 | E6/E12 prompts + conditions | 10 × 2 ÷ 3600 = 0.006/s | 150 × 2 ÷ 3600 = **0.08/s** | provider incident: 150 conditions in 10 s = 15/s, then 150 `cleared` | must-persist, never dropped | per agent `seq` | all browsers (Triage) |
| 9 | W3 usage readings + probe health | 3 × 6 ÷ 3600 = 0.005/s × 3 KB = 15 B/s; event probes ≤ 0.05/s; passive ≤ 4 ÷ 60 = 0.07/s | probes same 0.005/s (accounts do not scale); passive ≤ 50 ÷ 60 = **0.8/s**, 0.8 KB/s | goal start/finish wave: ≤ 1 per account per minute | must-persist (latest + history) | by `read_at` per account | all browsers (strip), coalesced |
| 10 | Commands, acks, progress, results | ≈ 0.05/s, 50 B/s | ≈ 0.5/s (dispatch ≤ 2/min + prompt answers 0.08/s + nudges + probes), **0.5 KB/s** | reconnect re-issue: pending outbox per machine (tens) | must-persist (outbox, results) | per epoch `seq`; results idempotent by id | Triage/Session (status) |
| 11 | W4 sync, GitHub polling | issues 60/h + claims 10 items × 12/h = 120/h + CR batch 60/h + full poll 2/h ≈ **250 req/h = 0.07 req/s**; ≈ 30 % non-304 × 20 KB = 1.5 MB/h | issues 5 × 60 = 300/h + claims 50 × 12 = 600/h + CR 300 CRs ÷ 50 per batch × 60 = 360/h + full 10/h ≈ **1,300 req/h = 0.36 req/s**, ≈ 8 MB/h | verify-now bursts ≤ 6/min | mirrors re-fetchable; cursors must-persist | per resource `updated_at` | Work view on change |
| 11b | W4 with webhooks (GitLab, Jira Cloud) | inbound ≈ 60 events/h | inbound ≈ **300 events/h = 0.08/s**; backstop poll every 10 min: 5 repos × 3 resources × 6/h = 90 req/h | provider burst (bulk label change): 100 events/s for seconds | inbound events can-lose (backstop poll repairs) | by `updated_at` | same |
| 12 | W9 terminal logs, shipped | 10 × 65 B/s = **0.65 KB/s** = 2.3 MB/h = 56 MB/day = 1.7 GB/month; 1 chunk/s | 300 × 65 = **19.5 KB/s** = 70 MB/h = 1.7 GB/day = **50 GB/month**; 30 chunks/s (30 index rows/s batched); all 500 captured: 32.5 KB/s = 117 MB/h = 84 GB/month | reconnect after 8 h offline: spool per machine ≤ 6 panes × 65 B/s × 28,800 s = 11 MB, shipped at line rate | must-persist (file + index), tolerates re-send | per stream byte offset | live tail only to viewers of that session; replay on demand |
| 12a | W9 raw into agent spools (not shipped) | 10 × 7.47 = 75 KB/s across agents | 300 × 7.47 = **2.24 MB/s** across agents (≤ 75 KB/s per machine); compressed in spool: 65 B/s per pane | — | agent-local | — | — |
| 13 | K3 transcripts (v3, opt-in) | 10 × 3.6 = 36 KB/s raw | 300 × 3.6 = **1.08 MB/s raw**; compression unmeasured | — | must-persist if enabled | by offset | replay only |
| 14 | J1 attach | 2 attaches × ≤ 7.5 KB/s = 15 KB/s | 10 × 7.5 = **75 KB/s** | redraw on resize | never dropped (PTY blocks) | per attach | 1:1 |
| 15 | W10/W11 settings, drift | rare | rare (≤ 100 catalog rows per change) | fleet-wide setting change: 50 Settings + 50 acks | must-persist | by generation | Machines and Settings views |

**Totals.**

| | A4 | A50 |
|---|---|---|
| Inbound to hub, steady | ≈ **9 KB/s** (8 summaries + 0.65 logs + 0.35 everything else) | ≈ **227 KB/s** (200 summaries + 19.5 logs + 7.5 everything else) ≈ 0.8 GB/h, of which 90 % is can-lose summaries |
| Durable bytes, steady | ≈ **0.8 KB/s** | ≈ **25 KB/s** (19.5 logs to files + ~5 events/state to the store) |
| Durable rows, steady | ≈ **2.4 rows/s** (log index 1, activity 0.33, heartbeat `last_seen` 0.27, resource 0.13, sync ≤ 0.5, rest ≈ 0.2) | ≈ **52 rows/s** (log index 30, sync ≤ 10, activity 5, heartbeat 3.3, resource 1.7, goal/branch 1, usage 0.8, commands 0.5) |
| Durable rows, peak | hub restart: 4 snapshots + replay ≤ 800 ev/s for seconds | hub restart: **2,500 ev/s for ~60 s** (budgeted), 50 snapshot txs; provider incident: 30 never-dropped events/s for 10 s |
| Store growth (retained) | events 0.4/s × 0.5 KB × 604,800 = **120 MB rolling week**; logs 1.7 GB/month | events 7/s × 0.5 KB × 604,800 = **2.1 GB rolling week** + 1 M resource rows; logs **50–84 GB/month** until the size cap |
| Browser fan-out | ≤ 20 KB/s | 2 MB/s unfiltered summaries; **≈ 140 KB/s** with per-pane subscriptions (120 summaries + 20 state) |
| Provider requests | 250 req/h | 1,300 req/h polling; ≈ 90 req/h + 300 events/h with webhooks |

## Questions step 4 must answer

Framed as requirements with numbers; step 4 chooses and records the technology
in `04-technology-review.md` and, where it differs from ADR 0001/0002, a
superseding ADR.

- **Q1 Durable store.** One writer must sustain ≈ 52 rows/s steady (A50) in
  batched transactions, and ≈ 2,500 rows/s for 60 s after a hub restart
  (batches of ≤ 100 rows → 25 tx/s), while a read pool serves ≤ 10 browsers
  at ≈ 1 list read/s each plus scheduler/verifier reads, without readers
  blocking the writer. Rolling data ≈ 2.1 GB/week of events plus mirrors,
  goals and audit (permanent, small). Single process, zero-dependency install
  (ADR 0001) is the standing constraint. Does the ADR 0002 default meet this,
  and at what batch size and sync mode?
- **Q2 Pane summary history.** Decided 2026-10-02 in
  [ADR 0013](../adr/0013-live-state-retention.md): latest summary per pane only
  (about 500 rows at A50); history comes from session logs. Step 4 sizes the
  store for overwrite-in-place at up to 100 updates/s, not a growing table.
- **Q3 Queue or broker.** Hub-internal flow is ≈ 110 messages/s steady and
  ≈ 10,000/s worst-case burst (replay before the per-hub budget) at A50; every
  never-dropped kind is already at-least-once from the agent's ring and every
  command is in the store's outbox, so no hub-side queue needs to be durable.
  Requirement: bounded per-agent queues with isolation (one slow agent never
  stalls another) and shedding only of the droppable kinds. Is an in-process
  channel design sufficient, or does anything need an external queue?
- **Q4 Hot state / cache.** The summary strip, the session sidebar (500
  panes) and pacing are read on every UI refresh: ≤ 10 reads/s of ≈ 500 rows
  each, target < 100 ms. Requirement: in-memory materialisation rebuilt from
  the store at start, or store reads fast enough; no separate cache process.
- **Q5 Browser fan-out.** 10 browsers; per-topic subscriptions (visible panes,
  goals, triage, strip) with latest-wins coalescing for summaries; ≈ 140 KB/s
  filtered, 2 MB/s if unfiltered; slow consumers drop summaries only; catch-up
  after reconnect by re-read. Which browser-facing stream (SSE, WebSocket,
  gRPC-web server streaming) and whether the same mechanism carries terminal
  bytes (75 KB/s, never dropped).
- **Q6 Log storage.** Append-only files at 19.5–32.5 KB/s (A50), 50–84
  GB/month before the size cap, 30 index rows/s, random access by `(session,
  stream, raw_offset)` and by time, retention by total size with oldest-first
  pruning and per-session delete, truncate-on-restart of a partial trailing
  frame. Files-plus-index per ADR 0010, or something else?
- **Q7 Replay budget.** Confirm per-agent (200 ev/s) and hub-wide (2,500
  ev/s) replay caps against the writer's measured batch throughput from Q1.
- **Q8 Provider limits and ingress.** Confirm current GitHub installation
  token limits and whether 304s are free (ADR 0005); decide whether the hub
  offers a webhook ingress (needs a public URL or tunnel) for GitLab/Jira
  Cloud, given polling alone is ≈ 1,300 req/h at A50.
- **Q9 Agent-side persistence.** The ring (≤ 1 week, size-capped; ≈ 0.1
  ev/s per machine), the command log (10,000 entries) and the compressed spool
  (65 B/s per pane) need an append-friendly local format with fsync batching
  ≤ 1 s; the same embedded engine as the hub, or plain files?

## Required contract changes

Found while writing the workflows. All nine were applied on 2026-10-02 in
the ADR 0018 change (additive within `fleet.agent.v1` and
`fleet.integration.v1`; ADR 0017 layout). The "Applied as" column names
what is in the protos now.

| # | Change | Reason (workflow) | Applied as |
|---|---|---|---|
| 1 | `Snapshot.prompts[] PromptDetected` and `Snapshot.conditions[] HarnessCondition` (currently showing) | W2, W7: rebuild Triage from the snapshot after a reconnect; the ring is size-capped and replay is throttled, and a prompt still on screen must be visible at once | as proposed (fields 23, 24); both messages moved to `agent/v1/screen.proto` to avoid an import cycle |
| 2 | `TermDown.select{window_id \| pane_id}` on the view session (or `SelectWindow`/`SelectPane` commands scoped to an `attach_id`) | W8: the window strip and pane switcher cannot type the developer's prefix key, which the hub does not know | `TermDown.select TermSelect{window WindowKey \| pane PaneKey}`, reusing the existing key types; hub API `AttachTerminalRequest.select` |
| 3 | `MachineFacts.username` (login user on the machine) | W8: the `ssh -t <user>@<host> tmux attach -t …` line; the hub never connects, so it must be rendered from facts | as proposed |
| 4 | `HelloAck.settings_restored` (bool) | W2, W10: after a hub restore from backup the generation goes backwards; the agent must accept a lower generation once | replaced by `settings_epoch` on `Hello`, `HelloAck`, `Settings`, `SettingsAck`; settings are ordered by `(settings_epoch, generation)` (02-contracts.md) |
| 5 | `StartGoalResult.typed_goal_line` (string, 4 KiB cap) | W5, ADR 0008: the consent actually written into each goal is recorded in the audit log; `GoalState.first_line` is what the hook wrote, not what was typed | as proposed; also `PreflightResult.goal_line_preview` for the dispatch preview |
| 6 | `SetSessionAvailability.reason` (string) | W6, W8: so `AvailabilityChanged.reason` can echo `goal_merged` vs `marked_on_board` | as proposed |
| 7 | `RepoCheckout.goal_hooks[] {harness, GoalHooksState}` | W5: the consent form (eshu `CONSENT:` line vs consent in the objective text) depends on whether that checkout ships its own hooks; today this is only learnable through `EnsureGoalHooks` | `RepoCheckout.goal_hooks[] HarnessGoalHooks{harness, state}` (`HarnessGoalHooks` in `fleet.type.v1`) |
| 8 | In the integrations PR: `ChangeRequest.review_rounds`, `ChangeRequest.linked_work_items[]`, `ChangeRequest.head_ref`/`head_sha`, `IntegrationCapabilities{webhooks, conditional_requests, batch_query, claims, labels, merge_state}`, `WorkItemRef.kind` including `CHANGE_REQUEST` | W4, W6: review-loop cap, verification by head ref, tracker/code-host pairing, "take over a change request no goal owns" | reconciled with what #7 (ADR 0012) already merged: `linked_work_items` (field 12), `head_ref` = `source_branch`, `head_sha` = `head_commit`, `webhooks`/`conditional_requests` = `sync_modes`, `claims` = `claim_mechanisms`, `labels`, `merge_state` = `merge_detection` and `WORK_ITEM_KIND_CHANGE_REQUEST` exist and were not duplicated; only `ChangeRequest.review_rounds` and `IntegrationCapabilities.batch_query` were added |
| 9 | Semantic clarifications, no fields: `LogAck` may be sent unsolicited (resume after pause) and with an empty `session_id` meaning all streams of the machine; `EnsureProbe.run_now` runs once per distinct `command_id`; the hub applies replayed events with `seq ≤ Snapshot.seq` to history only | W9, W3, W2 | written into 02-contracts.md (delivery semantics); "empty `session_id` = all streams" became an explicit `LogAck.all_streams` with `LogAck.key LogStreamKey` |

## Open questions for the owner

1. ~~Claude resume~~ **Answered by probe 2026-10-02:** the goal stays active
   after an interrupt and resumes when `continue` is typed (01b).
   Original question: **Claude resume.** Claude Code's `/goal` has set and clear only; pause is
   `Interrupt`. What resumes a Claude goal after an interrupt: a plain prompt
   ("continue"), re-typing `/goal <condition>`, or nothing needed because the
   Stop hook re-engages? Needs a probe like the 2026-10-01 Muse one.
2. ~~Codex interrupt~~ **Answered by probe 2026-10-02:** "Goal paused", no
   self-resume, `/goal resume` works (01b). Original question: **Codex interrupt.** Does an interrupt pause a Codex goal the way Muse's
   does, and must `/goal pause` be typed only at an idle prompt?
3. ~~Reserve line~~ **Decided 2026-10-02:** stop dispatching and alert;
   auto-pause is an opt-in setting, off by default (design.md rule 5).
4. **`max_parallel_goals_per_repo`** default (proposed 3) for merge churn.
5. ~~Pane summary retention~~ **Decided 2026-10-02:** latest only
   ([ADR 0013](../adr/0013-live-state-retention.md)).
6. **Claims.** Default claim-comment pattern (eshu's exact text), stale-claim
   age before an owner is asked, and the claim mechanism for Jira (assignee?
   transition?) and GitLab.
7. **Log cadence and ack.** 10 s / 256 KiB chunks; ack after file write and
   index commit (≤ 1 s batches). Acceptable loss on agent crash ≤ 10 s.
8. **Probe placement.** Prefer `always-on` labelled machines, then the hub
   machine; is the hub machine acceptable as a prober by default?
9. **"Approved and green"** (no merge consent): `review_decision = APPROVED`
   and `checks.rollup = SUCCESS` → `ready_for_human_merge`. Correct?
10. **Heartbeat `last_seen`** persisted per heartbeat (3.3 rows/s at A50) or
    memory only with a periodic write?
11. **Transcripts (K3).** Measure compression of Claude/Codex/Muse transcript
    files before v3; at A50 the raw rate (1.08 MB/s) exceeds terminal logs.
12. **Unsupported harness version, manual dispatch.** Allowed with a warning
    and counted as 1 active against `allowed_active` (proposed).
13. **Webhook ingress** for GitLab/Jira Cloud: offer a tunnel-based public
    endpoint, or polling only in v1?
14. **Replay budgets** (200 ev/s per agent, 2,500 ev/s per hub) as proposed?
