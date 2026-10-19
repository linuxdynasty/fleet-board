// Package store opens one SQLite file (state.db, logs.db or agent.db) the way
// ADR 0014 decides and owns its single writer (docs/specs/03f-hub-internals.md).
//
// [Open] sets the ADR 0014 pragmas on every connection (WAL, synchronous
// FULL, fullfsync on, busy_timeout 5000, foreign keys, a 64 MiB journal size
// limit; readers are also query_only; logs.db adds auto_vacuum=INCREMENTAL
// before its first table, ADR 0015), applies the forward-only [Migration]
// list, opens a read pool of at most 10 connections and starts the writer
// goroutine. With Options.MustExist it opens an existing file only (SQLite's
// mode=rw) and returns [ErrMissing] instead of creating one.
//
// Every write is a job ([ApplyFunc]) sent with [Store.Submit] or
// [Store.Write]. The writer collects jobs until the batch holds 100 of them or
// 250 ms have passed since its first job, then runs them in one BEGIN
// IMMEDIATE transaction, each under its own savepoint, and commits. A job's
// outcome is sent only after the commit is durable, so an acknowledgement
// that follows it is never ahead of the disk. A job that fails is rolled back
// alone; a batch whose transaction breaks fails every job in it.
// After each successful outer commit, before job outcomes, the writer offers
// a nonblocking hint on [Store.CommitWake]. One future hub Watch dispatcher
// consumes it and reads committed state; hints can coalesce. The writer closes
// the channel after the final flush during [Store.Close].
// Cancellation after admission does not discard a job or its committed wake;
// its regression test cancels inside a bounded callback without waiting.
// BenchmarkWriterBatch times one such batch at 1, 10 and 100 jobs.
//
// Reads go through [Store.Read], a short read transaction on the pool that
// sees the last commit and never waits for the writer.
//
// The Submit that fills the last slot, or one that finds no room, stamps
// when the queue became continuously full; the writer clears the stamp
// when it takes a job and the queue has room. [Store.QueueFullSince]
// reports the stamp (zero when the queue is not full), the
// WRITER_QUEUE_FULL reading of 05b.
//
// [VacuumInto] copies a file with VACUUM INTO on its own read-only (mode=ro)
// connection, never the pools (VACUUM INTO fails on a query_only one), and
// [OpenReadOnly] opens a copy to check it; backups use both.
package store
