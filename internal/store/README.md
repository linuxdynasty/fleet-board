# internal/store

One SQLite file with one writer goroutine and a small read pool, as ADR 0014
and [03f](../../docs/specs/03f-hub-internals.md) describe. The hub uses it for
`state.db` (and later `logs.db`), each agent for `agent.db`. The driver is
`modernc.org/sqlite` (pure Go, no cgo).

## Use

```go
s, err := store.Open(ctx, store.Options{
	Path: filepath.Join(stateDir, "state.db"),
	Name: "state", // the db label: state, logs or agent
	Migrations: migrations,
	Telemetry: tel,
})
defer s.Close()

err = s.Write(ctx, func(tx *sql.Tx) error { // waits for the durable commit
	_, err := tx.Exec("INSERT INTO ...")
	return err
})
done, err := s.Submit(ctx, apply) // queue now, read the outcome from done later
err = s.Read(ctx, func(tx *sql.Tx) error { ... }) // short read on the pool
wake := s.CommitWake() // one future Watch dispatcher reads this hint
```

## What it guarantees

- **Pragmas on every connection:** `journal_mode=WAL`, `synchronous=FULL`,
  `fullfsync=ON` (macOS: commits use `F_FULLFSYNC`), `busy_timeout=5000`,
  `foreign_keys=ON`, `journal_size_limit=64 MiB`; readers add `query_only`.
  A `logs` store also sets `auto_vacuum=INCREMENTAL` (ADR 0015) before its
  first table.
- **One writer.** Only the writer goroutine writes, on one connection, with
  `BEGIN IMMEDIATE`. A batch is at most 100 jobs, or what arrived within
  250 ms of its first job, in one transaction.
- **Ack after commit.** A job's outcome (`Write`'s return, `Submit`'s channel)
  arrives only after its batch commits. A job that returns an error is rolled
  back to its own savepoint and the rest of the batch commits. If the
  transaction itself breaks, every job in the batch gets the error.
- **Commit wake.** Each successful outer batch commit offers a nonblocking
  hint on `CommitWake` before job outcomes. The capacity-one channel coalesces
  hints; one hub Watch dispatcher reads committed rows to find changes. A
  failed outer commit sends no hint. A no-op committed batch may send one.
  `Close` flushes queued jobs, then closes the channel. See [commit.md](commit.md)
  for the ownership and ordering diagram. Cancellation after admission does not
  discard the job or suppress its committed wake; the cancellation test uses
  a bounded callback that never waits on another goroutine.
- **One change, one job.** A lone job waits up to the 250 ms window for
  company before it commits, so a loop of `Write` calls costs up to 250 ms
  each. Put related changes in one job, or `Submit` them all and then wait.
- **Jobs stay local.** A job runs inside the transaction: no network call, no
  waiting on other goroutines, no `Commit` or `Rollback`. Do the round trip
  first, then submit the result.
- **Backpressure.** The queue holds 1,000 jobs; `Submit` waits when it is full,
  until its context ends. The `Submit` that fills the last slot, or one
  that finds no room, stamps the start of the full period
  (`QueueFullSince`); the writer clears it when it takes a job and the
  queue has room, so only an unbroken full stretch counts.
- **Readers** (at most 10) see the last commit and never wait for the writer.
- **Migrations** are forward-only, numbered from 1, each applied with its
  `schema_migrations` row in one transaction. `Open` refuses a file whose
  version is newer than the binary (`ErrSchemaTooNew`) or whose applied
  migration SQL was edited (`ErrMigrationChanged`, by checksum).
- **Close** commits every queued job, then refuses new work (`ErrClosed`).
- **`MustExist`** opens the file read-write without ever creating it (SQLite's
  `mode=rw`, through an escaped `file:` URI): a missing file is `ErrMissing`
  and nothing appears on disk. `fleetd run` opens `state.db` this way.
- **Copies.** `VacuumInto(ctx, src, dst)` writes a consistent copy of a file
  with `VACUUM INTO` on its own read-only connection (SQLite's `mode=ro`),
  outside any transaction and outside the store's pools, so it works with or
  without a `Store` open on `src` (committed WAL rows are copied) and never
  writes `src`. The read pool cannot do this: `VACUUM INTO` fails on a
  `query_only` connection. `dst` must be missing or empty, so a copy never
  overwrites a file. `OpenReadOnly(path)` opens an existing file `mode=ro`
  and `query_only` to check a copy; it never creates one. The hub's backups
  use both ([internal/hub/backup](../hub/backup/README.md)).

## Signals

Metrics carry `db="state|logs|agent"` and the program's prefix (`fleet_hub`
or `fleet_agent`): `_writer_batch_messages`, `_writer_batch_seconds`,
`_writer_queue_age_seconds`, `_writer_jobs_total{result}` (`committed`,
`failed`, `aborted`), `_writer_oldest_job_age_seconds` and
`_writer_queue_depth` (gauges that rise while the writer is stuck),
`_writer_queue_full_seconds` (how long the queue has been continuously full,
0 when it is not),
`_writer_queue_full_total` (submits that gave up on a full queue),
`_read_pool_wait_seconds`, `_store_bytes`, `_store_wal_bytes`. Log events: `store.opened`, `store.migrated`,
`store.batch_failed` (with `err_kind` and `sqlite_code`, SQLite's numeric
result code), `store.closed`. No paths or SQL text
are logged.

No-Observability-Change: `fleet_hub_writer_batch_messages`,
`fleet_hub_writer_batch_seconds` and `fleet_hub_writer_jobs_total{result}`
already show whether hub batches commit or fail (with the matching
`fleet_agent_` series on agents); the wake itself carries no resource identity
and does not change a row.

## Tests

`CGO_ENABLED=0 go test ./internal/store/` (add `-race`). Every test uses a
real file in `t.TempDir()`. `TestAckedWritesSurviveAKilledProcess` kills a
child process with SIGKILL mid-write and checks every acknowledged row after
reopening. `TestDurableCommitsUseFullFsyncOnDarwin` repeats spike S1's timing
check: a store commit must take at least 1 ms (about 4 ms with `F_FULLFSYNC`,
under 0.2 ms without, both logged). It builds only on macOS, so it runs on
macOS dev machines and not in CI (Ubuntu); `-short` skips it.

`TestWriterMeetsTheA50RestartBurst` replays spike S1's restart burst (about
2,600 jobs a second for 10 s, one job per message) through Submit and checks
the writer keeps up, stays under 70 % busy and commits each job within 1 s.
It measures wall-clock time, so it runs only when asked:
`FLEET_STORE_A50=1 CGO_ENABLED=0 go test -count=1 -run A50 -v ./internal/store/`.

`BenchmarkWriterBatch` times one full batch through Submit at 1, 10 and 100
jobs (`BatchMax` set to the size, so each op is one commit) and reports
allocations. It is the input to `make bench-report`, which a pull request runs
against its base; it is not a budget check. On an M4 Pro a commit takes about
4 ms (ADR 0014's durable commit): `CGO_ENABLED=0 go test -run '^$' -bench
WriterBatch -benchmem ./internal/store/`.
