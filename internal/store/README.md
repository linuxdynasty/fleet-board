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
```

## What it guarantees

- **Pragmas on every connection:** `journal_mode=WAL`, `synchronous=FULL`,
  `fullfsync=ON` (macOS: commits use `F_FULLFSYNC`), `busy_timeout=5000`,
  `foreign_keys=ON`, `journal_size_limit=64 MiB`; readers add `query_only`.
- **One writer.** Only the writer goroutine writes, on one connection, with
  `BEGIN IMMEDIATE`. A batch is at most 100 jobs, or what arrived within
  250 ms of its first job, in one transaction.
- **Ack after commit.** A job's outcome (`Write`'s return, `Submit`'s channel)
  arrives only after its batch commits. A job that returns an error is rolled
  back to its own savepoint and the rest of the batch commits. If the
  transaction itself breaks, every job in the batch gets the error.
- **Jobs stay local.** A job runs inside the transaction: no network call, no
  waiting on other goroutines, no `Commit` or `Rollback`. Do the round trip
  first, then submit the result.
- **Backpressure.** The queue holds 1,000 jobs; `Submit` waits when it is full,
  until its context ends.
- **Readers** (at most 10) see the last commit and never wait for the writer.
- **Migrations** are forward-only, numbered from 1, each applied with its
  `schema_migrations` row in one transaction. `Open` refuses a file whose
  version is newer than the binary (`ErrSchemaTooNew`) or whose applied
  migration SQL was edited (`ErrMigrationChanged`, by checksum).
- **Close** commits every queued job, then refuses new work (`ErrClosed`).

## Signals

Metrics carry `db="state|logs|agent"` and the program's prefix (`fleet_hub`
or `fleet_agent`): `_writer_batch_messages`, `_writer_batch_seconds`,
`_writer_queue_age_seconds`, `_writer_jobs_total{result}` (`committed`,
`failed`, `aborted`), `_read_pool_wait_seconds`, `_store_bytes`,
`_store_wal_bytes`. Log events: `store.opened`, `store.migrated`,
`store.batch_failed` (with `err_kind`), `store.closed`. No paths or SQL text
are logged.

## Tests

`CGO_ENABLED=0 go test ./internal/store/` (add `-race`). Every test uses a
real file in `t.TempDir()`. `TestAckedWritesSurviveAKilledProcess` kills a
child process with SIGKILL mid-write and checks every acknowledged row after
reopening. On macOS, `TestDurableCommitsUseFullFsyncOnDarwin` repeats spike
S1's timing check (about 4 ms per commit with `F_FULLFSYNC`, under 0.1 ms
without); `-short` skips it.
