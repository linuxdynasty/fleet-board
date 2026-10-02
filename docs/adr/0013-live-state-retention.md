# 0013. Live-state retention: latest pane summary only

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Amends: [0010](0010-session-logs.md) (the "live state kept 1 week" row, for pane summaries and heartbeats)

## Context

ADR 0010 kept all live state, including pane summaries, for one week. The step 3
load model ([03g](../specs/03g-load-and-handoff.md)) shows that at 50 machines
× 10 panes, a week of pane summaries is about 8.6 million rows and 17 GB per
day, most of it redundant snapshots of screens that barely changed. Session
logs ([0010](0010-session-logs.md)) already keep the full terminal history for
reading, search and replay.

## Decision

| Data | Kept | Why |
|---|---|---|
| Pane summaries | **latest per pane only** (overwritten in place) | the board shows the current screen; history lives in session logs |
| Heartbeats | **latest per machine only**; `last_seen` written periodically, not per heartbeat | liveness needs the latest value |
| Agent offline buffer (event ring) | 1 week, size-capped | unchanged from 0010; needed to backfill after a hub outage |
| Goal state, triage, audit, usage readings, commands | as before | durable records |
| Session logs and transcripts | as in 0010 (configurable, size cap) | the history for research and replay |

## Consequences

- At 50 machines the pane-summary table stays at about 500 rows instead of
  millions; storage for live state no longer grows with time.
- Anything you'd want to read later (what a pane showed an hour ago) comes from
  the session log replay, not from summaries.
- If a history of summaries is ever needed, it is derived from session logs,
  not stored twice.
