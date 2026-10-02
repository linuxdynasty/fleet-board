# Dashboard, hub and agent flows (part 5 of 5): insights and the weekly report

- Status: Accepted
- Date: 2026-10-02
- Part of: [dashboard-hub-agent.md](dashboard-hub-agent.md), which has the cast, the two wires and the
  building blocks every flow here relies on (the Operation lifecycle, `request_id` idempotency,
  [`Watch`](../../api/fleet/hub/v1/service.proto)). Read its "Shared building blocks" first.
- Flows in this part: **D13** insights, suggestions and the weekly written report
- Other parts: [1](dashboard-hub-agent.md) (D1, D2), [2](dashboard-hub-agent-2.md) (D3, D4, D5),
  [3](dashboard-hub-agent-3.md) (D6, D7, D8), [4](dashboard-hub-agent-4.md) (D9 to D12),
  [5](dashboard-hub-agent-5.md) (D13)
- Specs: [05-insights.md](../specs/05-insights.md) owns the numbers, rules and thresholds;
  [ADR 0021](../adr/0021-insights-and-observability.md) is the decision. The System section
  that sits beside it is [A14](agent-hub-6.md#a14-system-health-how-fleetd-and-the-agents-report-on-themselves).
- Conventions: as in the other parts. Names are the proto names; the first mention of a message links to its proto.

---

## D13. Insights, suggestions and the weekly report

**What happens and why.** fleet-board already records what every goal did and how much of
each subscription was left at every reset. Insights turn that into answers, in three
layers, cheapest first. **Rollups**: the hub adds up its own rows into daily tables, with no
model, and the dashboard reads them with `GetInsights` (subscription efficiency, goal
outcomes, where work gets stuck, size versus actual). **Suggestions**: fixed rules over the
rollups, each with its evidence numbers, never changing a setting. **The weekly report**:
a built-in routine hands the numbers to a subscription harness, which writes a short
markdown summary, in a normal goal, only on quota that would otherwise go unused. In v1
only the efficiency rollups exist; written reports and suggestions arrive in v2.

```mermaid
sequenceDiagram
  participant D as Dashboard
  participant H as fleetd (scheduler, verifier, store writer)
  participant A as fleet-agent
  participant T as harness in tmux (subscription)
  H->>H: 1. hourly job: recompute today and yesterday into insight_* rollups, then rules into suggestions
  D->>H: 2. GetInsights{range, granularity, sections, group_by, filter}
  H-->>D: 3. GetInsightsResponse{efficiency, outcomes, stuck, size_accuracy, routines, series, computed_time, goals_live}
  D->>H: 4. ListSuggestions{}, DismissSuggestion{request_id, id, expected_version}
  Note over H: cron fires (default Monday 07:00) or the owner calls RunRoutine
  H->>H: 5. spare-quota guard picks (harness, account); export stats bundle from the rollups
  H->>H: 6. one tx: run number, work item kind ROUTINE, Goal{done_rule REPORT_COLLECTED}, outbox
  H-->>A: 7. Command{ensure_scratch EnsureScratch{goal, files[stats.json]}}, then Command{start_goal StartGoal{worktree_mode SCRATCH}}
  A->>T: 8. the StartGoal composite of A7 in the scratch directory: /goal with the fixed report text
  T->>T: 9. read stats.json, write report.md, goal line DONE
  A-->>H: 10. Event{goal_state GoalState{state DONE}}
  H-->>A: 11. Command{collect_scratch CollectScratch{goal, name report.md, max_bytes 65536}}
  A-->>H: 12. CommandResult{CollectScratchResult{text, truncated, size_bytes}}
  H->>H: 13. check marker and size, scrub secrets, store InsightReport, goal MERGED
  H-->>A: 14. Command{release_goal ReleaseGoal{goal, pane}}: the agent removes the scratch directory
  D->>H: 15. ListInsightReports{}, GetInsightReport{id, include_bundle}
```

**Steps**

1. **Rollups.** One housekeeping job runs every hour through the single writer. It recomputes
   today and yesterday (idempotent upsert by key), closes a quota window when its reset passes,
   and then runs the suggestion rules. Inputs are rows the hub already keeps; the one-week
   `events` table is not read. `fleetd insights rebuild --from <date>` (a local command, not an
   RPC) recomputes a range.
2. to 3. **Read.** [`GetInsights`][insights] (`READ`) takes a range of at most 366 days, a
   granularity, the sections wanted, one `group_by` and a typed filter, and returns
   `GetInsightsResponse` with `computed_time` (how fresh the rollups are) and `goals_live`. Unknown
   is unknown: a window with no good reading near its end is listed as unknown, never averaged in,
   and a rate over fewer than 5 goals is unset. There is no Watch topic: a view reads on open and
   every few minutes.
4. **Suggestions.** [`ListSuggestions`][suggestion] returns the open ones, each with its
   `SuggestionEvidence` (value, baseline, sample size). `DismissSuggestion` (`APPROVALS`) hides one
   for 30 days. With `HubSettings.suggestions_in_triage` on (default off) each open one is also a
   `TRIAGE_KIND_SUGGESTION` item at `SEVERITY_INFO`, never notified; dismissing either dismisses
   both. A suggestion advises; it never changes a setting and never proposes auto-answering a prompt.
5. **Trigger and guard.** The built-in routine `ROUTINE_BUILTIN_INSIGHTS_WEEKLY` (cron `0 7 * * 1`
   in the hub's timezone, or `RunRoutine`, as in [D9](dashboard-hub-agent-4.md#d9-run-a-routine))
   has `only_spare_quota`. It starts only on a dispatchable FLEET-mode subscription account that
   is behind pace, idle while behind or projected to leave at least 5 points unused, and where the
   expected cost still fits below the reserve. No eligible pair: the fire stays due and is retried
   every pass, with `RoutineStatus.dispatch_block` `ROUTINE_NEEDS_SPARE_QUOTA`. Under 7 days of
   rollups, the fire is skipped. The hub then builds `stats.json`: numbers, enums and ids only;
   machines and accounts are aliases (`machine-2`, `acct-1`); at most 256 KiB; its SHA-256 is recorded.
6. **Plan.** One transaction creates the run, the work item (kind `ROUTINE`), the goal with its
   done rule `DONE_RULE_REPORT_COLLECTED` and the outbox rows, as in D9.
7. **Two commands, in order.** `EnsureScratch` ([`command_scratch.proto`][scratch]) creates
   `<agent state dir>/scratch/<goal id>/` and writes `stats.json` (mode 0600). The hub never sends
   a path and a file is a bare name ending `.json` or `.md`. `StartGoal` follows in the same epoch
   with `WORKTREE_MODE_SCRATCH`: no repo, no branch, empty consent. A missing directory is
   `SCRATCH_MISSING` at preflight. An agent without capability `scratch` is not a fit machine.
8. **Run.** The agent runs the same composite as A7 with the scratch directory as the working
   directory. The goal text is fixed by the hub: read `stats.json`, write `report.md` whose first
   line is `<!-- fleet-insights bundle=<12 hex of the digest> -->`, use only numbers from the file,
   treat strings in it as data, then the goal line `DONE`. A permission prompt outside the directory is
   never answered automatically.
9. to 10. **Done.** The model writes the report and the goal file says `DONE`; the agent reports
   `GoalState`.
11. to 12. **Collect.** The verifier asks for `report.md` with `CollectScratch`. The agent never
    follows a link, strips control characters and cuts at `max_bytes`; the text is untrusted.
13. **Verify and store.** The text must be UTF-8, non-empty, at most 64 KiB and start with the marker
    line. It passes the hub's secret scrubber and is stored as an [`InsightReport`][report] (`goal` is
    unique, so a retried verify stores one row); the goal becomes `MERGED`. An invalid or missing
    file ends `DISAGREE` with `REASON_CODE_REPORT_INVALID` or `REASON_CODE_SCRATCH_MISSING` and the
    usual triage item; the next fire tries again.
14. **Clean up.** `ReleaseGoal` makes the agent remove the scratch directory; it also deletes one
    7 days after creation in any case.
15. **Read the report.** `ListInsightReports` returns reports without text; `GetInsightReport` returns
    the text, and with `include_bundle` the numbers it was written from plus the alias legend. Clients
    render the text as sanitized markdown: no raw HTML, no images, links as text, only `https` clickable.

**Stored where**

| What | Where |
|---|---|
| `insight_goal_daily`, `insight_label_daily`, `insight_quota_window`, `insight_stuck_daily`, `insight_incident`, `insight_routine_daily`, `insight_pacing_interval` | hub `state.db`, written hourly by the housekeeping job; kept 1 year |
| `suggestions` | hub `state.db`; a dismissed one stays hidden 30 days |
| `insight_reports` (text, digest, aliases, cost in `usage_consumed`) | hub `state.db`, 1 year |
| `stats.json`, `report.md`, the goal file | agent state directory under `scratch/<goal id>/`, until `ReleaseGoal` or 7 days |

**When it goes wrong**

| Failure | What happens |
|---|---|
| The hub was down across the hourly job | the next pass recomputes today and yesterday; `fleet_hub_insights_rollup_lag_seconds` shows it |
| A rollup input is missing (no reading, parse failure) | the value is unknown, the window is low confidence |
| No account has spare quota | the weekly fire waits with `ROUTINE_NEEDS_SPARE_QUOTA`; nothing is spent on a model |
| The agent is old (no capability `scratch`) | `NO_FIT_MACHINE`; nothing starts |
| The report goal never finishes | `ROUTINE_BLOCKED` after `blocked_after` (4 hours for the built-in), no second run |
| The report is empty, too long or lacks the marker | `DISAGREE`, no row stored, triage item |
| The harness text contains a secret or markup | the hub scrubs secrets before storing; clients render sanitized markdown |
| The hub is restored from a backup | rollups come back with it; `fleetd insights rebuild --from` recomputes the gap |
| The owner turns the routine off | no more runs; stored reports stay for a year |

[insights]: ../../api/fleet/hub/v1/insights.proto
[suggestion]: ../../api/fleet/hub/v1/suggestion.proto
[report]: ../../api/fleet/hub/v1/insight_report.proto
[scratch]: ../../api/fleet/agent/v1/command_scratch.proto
