# 0014. Storage confirmed: embedded SQLite, settings from step 4

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Confirms: [0001](0001-system-architecture.md) (no Redis, no broker, no
  Postgres) and [0002](0002-storage-sqlite.md) (SQLite), which were
  provisional until step 4

## Context

ADR 0002 chose embedded SQLite before the load was known and left the driver
and its WAL behavior to a spike. Step 3 ([03g](../specs/03g-load-and-handoff.md))
set the load: ≈ 52 rows/s steady and 2,500 rows/s for 60 s at 50 machines,
≤ 10 browsers reading. The hub is often a laptop, where `fsync()` alone does
not reach permanent storage.

## Options

1. SQLite with a pure-Go driver (`modernc.org/sqlite` or `ncruces/go-sqlite3`).
2. Postgres.
3. SQLite plus Redis for hot state and pub/sub, or plus an embedded broker.

## Decision

Option 1, measured in spike S1 ([04](../specs/04-technology-review.md#q1-durable-store)).

| Setting | Value |
|---|---|
| Driver | `modernc.org/sqlite` (fallback `ncruces/go-sqlite3`, same `store` interface) |
| Files | `state.db` (everything in 03f except logs); `logs.db` ([0015](0015-session-logs-in-sqlite.md)); `agent.db` on each agent |
| Pragmas | `journal_mode=WAL`, `synchronous=FULL`, `fullfsync=ON`, `busy_timeout=5000`, `foreign_keys=ON`, `journal_size_limit=64 MiB` |
| Writer | one connection per file, `BEGIN IMMEDIATE`, batches of ≤ 100 messages or 250 ms |
| Readers | ≤ 10 `query_only` connections; short read transactions |
| Hot state | read from the store; no cache process |
| Queues | in-process bounded channels (03f); no broker |
| Backup | nightly `VACUUM INTO` of `state.db`; `logs.db` only on request |

## Consequences

- A durable commit costs ≈ 4 ms on the hub's SSD (`F_FULLFSYNC`); the writer
  spends ≈ 3 % of its time on commits at 50 machines steady and ≤ 70 % during
  the restart burst.
- The 03f batch window changes from 50 ms to 250 ms: about 270 ms from event
  to commit at p99, a third fewer disk writes.
- Install stays one binary per role with nothing else to run.
- Postgres remains possible behind the `store` interface for more than one hub.

- Measured on the hub MacBook (APFS) and on omarchy (Linux, btrfs) with the
  same results within 2× (04, Q1).

## Open questions

- `logs.db` at a 50 GB size (S1 used 2 GB; 04, "Not proven").
