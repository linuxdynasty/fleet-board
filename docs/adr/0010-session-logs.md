# 0010. Session logs and retention

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

The owner wants the entire history of every session stored so it can be loaded
later to research, read and learn from. Live state (pane summaries, the agent's
offline buffer) is short-lived operational data; session history is a
long-lived record. They need different storage and retention.

## Decision

**Two kinds of data.**

| Data | Kept | Where |
|---|---|---|
| Live state: pane summaries, agent offline buffer, heartbeats | 1 week | hub SQLite; agent ring buffer |
| Session logs: full terminal output, plus harness transcript references | configurable, default kept with a size cap | files on the hub's disk, indexed in SQLite |

**Capture.** For every session on the fleet tmux server, the agent records the
pane's full output (tmux `pipe-pane`) to a local spool, and records where the
harness keeps its own transcript for that session (Claude Code session files,
Codex rollout logs, Muse sessions), which are richer than terminal output.
Harness transcripts are read only from the harness's session directory, never
from credential or config files.

**Shipping.** The agent sends compressed chunks to the hub in the background
over a separate stream on its connection, with byte offsets so a reconnect
resumes without gaps or duplicates. The agent keeps its spool until the hub
acknowledges each chunk, capped by size; if the cap is hit it keeps the newest
data and marks the gap. Logs remain available after the worker goes offline.

**Storage.** One append-only compressed file per session per day on the hub,
indexed in SQLite (session, machine, goal, time range, byte offsets, size).
Harness transcripts are copied alongside when their session ends.

**Reading.** The UI shows a session's history with time navigation, search
across sessions, and a terminal-style replay. The CLI exports a session
(`fleet logs export <session>`).

**Retention.** Configurable. Default: keep, with a total size cap on the hub;
the oldest sessions are pruned first. Per-session delete in one action.

**Sensitive content.** Terminal output can contain anything printed on screen,
including tokens or secrets. Logs are stored only on the hub, readable only by
the hub user and people with the `owner` role; encryption at rest is an option;
deleting a session's logs is one action and is audited.

## Consequences

- Disk use on the hub grows with activity; the size cap and UI show usage.
- Replay quality depends on raw terminal output; harness transcripts give the
  structured view (messages, tool calls).

## Open questions

- Default size cap, and whether to compress older logs further.
- Whether to redact obvious secret patterns before storage (risk of false
  confidence) or only offer encryption and deletion.
