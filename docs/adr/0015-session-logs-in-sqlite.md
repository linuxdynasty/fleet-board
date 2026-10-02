# 0015. Session logs stored in logs.db

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Supersedes, in part: [0010](0010-session-logs.md) ("Storage": one file per
  session per day on disk, indexed in SQLite)
- Amends: [0013](0013-live-state-retention.md) (pane summaries: archived at
  session end)

## Context

ADR 0010 stored session logs as append-only zstd files on the hub disk with an
index in SQLite. Writing the file and the index separately needs a repair
step after a crash (truncate a partial trailing frame), and every file touched
in a batch needs its own sync (≈ 4 ms each on the hub's SSD). Chunks are small
(hundreds of bytes to a few KB after compression).

## Options

1. Files per `(session, stream, day)` plus an index in `state.db` (ADR 0010).
2. Chunks as rows in a second SQLite file, `logs.db`.
3. Seekable zstd files.

## Decision

Option 2, measured in spike S1
([04](../specs/04-technology-review.md#q6-session-log-storage)).

- `logs.db` holds `log_sessions` and `log_chunks{session, stream, raw_offset,
  raw_length, first_at, last_at, gap_before, data}` with a unique
  `(session, stream, raw_offset)`. Chunk and index are one row, so they cannot
  disagree after a crash.
- Its own writer goroutine; one transaction per ≤ 1 s batch across all
  streams; `LogAck` after commit; same pragmas as `state.db` plus
  `auto_vacuum=INCREMENTAL`.
- Framing (spike S2): the agent keeps one zstd frame per stream open for at
  most 5 minutes or 4 MiB raw and ships a flush every 10 s as one chunk;
  chunks carry `frame_start` and `frame_raw_offset`, and replay decodes from
  the start of the frame. 14× on Codex output, against 8× for independent
  10 s frames.
- Retention: default size cap 50 GB (50–270 GB/month at 50 machines,
  depending on the harness mix; 2–9 GB/month at 4). Delete the oldest `FINAL` sessions until under the size cap, then
  `incremental_vacuum` in 2,000-page steps. Per-session delete is one
  transaction, audited in `state.db`.
- **Pane summaries (amends 0013):** every update is written to `state.db`
  (latest per pane). When a session ends, the last summary of each of its panes
  moves to `logs.db` with the session's log and is pruned with it.
- Export and replay go through the hub (`fleet logs export`); files on disk are
  not an interface.

## Consequences

- One sync per batch instead of one per file; no startup repair.
- `state.db` backups stay small; `logs.db` is backed up only on request.
- Logs are not readable with the `zstd` CLI directly; export is the way out.
- Sensitive-content rules of 0010 (owner-only, delete is one audited action)
  are unchanged; encryption at rest, if added, applies to the file.
