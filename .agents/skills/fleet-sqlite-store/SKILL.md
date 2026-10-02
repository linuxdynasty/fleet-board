---
name: fleet-sqlite-store
description: "Use when writing or changing code that touches state.db, logs.db or agent.db: the single writer goroutine, WAL and durability pragmas, BEGIN IMMEDIATE batches, forward-only migrations, and which data lives in which file."
---

# SQLite store

Authority: [ADR 0014](../../../docs/adr/0014-storage-confirmed.md),
[ADR 0015](../../../docs/adr/0015-session-logs-in-sqlite.md),
[04-technology-review.md](../../../docs/specs/04-technology-review.md) (Q1, Q6, Q9),
[03f-hub-internals.md](../../../docs/specs/03f-hub-internals.md). Driver:
`modernc.org/sqlite` (pure Go, no cgo); fallback `ncruces/go-sqlite3` behind the
same `store` interface.

## Files

| File | Owner | Holds |
|---|---|---|
| `state.db` | hub | everything in 03f except logs (machines, sessions, goals, outbox, triage, mirrors, audit, latest pane summaries) |
| `logs.db` | hub | `log_sessions`, `log_chunks` (chunk and index in one row), archived last pane summaries |
| `agent.db` | each agent | command log, event ring, spool chunks |

Raw `pipe-pane` output stays in plain append files on the agent. Keep a new
table in the file that matches its lifecycle and backup rule (`state.db` backed
up nightly with `VACUUM INTO`; `logs.db` only on request).

## Writer rules

- **One writer goroutine per file**, one connection, `BEGIN IMMEDIATE`. Every
  write is a job sent to it; nothing else holds a write transaction.
- Batch up to 100 messages or **250 ms** into one transaction; `logs.db` one
  transaction per batch of at most 1 s across streams. Derived state (goal,
  triage, availability) is computed inside the transaction from the current
  row and the event, as a pure function.
- **A transaction never spans a network call.** Do the round-trip, then write
  the result. Commands are enqueued inside the commit transaction and handed to
  the connection writer after commit.
- Ack after commit: `LogAck`, `CommandAck` and `ResultAck` follow a durable
  commit, never precede it.
- Readers: at most 10 `query_only` connections, short read transactions.
  Scheduler and verifier read a consistent snapshot, then submit write jobs.
- Backstop every ownership rule with a unique constraint
  ([03f race table](../../../docs/specs/03f-hub-internals.md#what-can-race-and-what-backs-it-up)),
  for example one live goal per work item and `(session, stream, raw_offset)`
  on log chunks. Handle the constraint error as a normal outcome.

## Pragmas (set on every connection, tested)

`journal_mode=WAL`, `synchronous=FULL`, `fullfsync=ON` (macOS needs it for a
durable commit), `busy_timeout=5000`, `foreign_keys=ON`,
`journal_size_limit=64 MiB`; `logs.db` adds `auto_vacuum=INCREMENTAL`. Do not
weaken durability to make a benchmark pass; the cost is measured (about 4 ms
per commit) and accepted in ADR 0014.

## Migrations

- Forward-only numbered migrations applied at startup inside a transaction,
  each recorded in a schema table. Never edit an applied migration; add a new
  one. No down migrations.
- A migration that rewrites a large table or changes `logs.db` needs a size
  and time measurement on realistic data (`fleet-performance-rigor`).
- The binary refuses to start on a database whose schema version is newer than
  it understands.

## Tests

Test against a real SQLite file in `t.TempDir()`, not a mock. Required cases:
crash between commit and ack (reopen and replay), duplicate delivery hitting
the unique constraint, batch boundary at 100 messages and 250 ms, concurrent
readers during a write, and migration from an empty database and from the
previous schema. Run with `-race`.

## Evidence limits

The numbers in 04 were measured on the hub MacBook (APFS) and a Linux btrfs
host with spike S1 (`spikes/sqlite/`); `logs.db` at 50 GB is not proven. Do not
claim a throughput or durability result outside that without measuring it.
