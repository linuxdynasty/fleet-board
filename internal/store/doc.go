// Package store opens one SQLite file (state.db, logs.db or agent.db) the way
// ADR 0014 decides and owns its single writer (docs/specs/03f-hub-internals.md).
//
// [Open] sets the ADR 0014 pragmas on every connection (WAL, synchronous
// FULL, fullfsync on, busy_timeout 5000, foreign keys, a 64 MiB journal size
// limit; readers are also query_only; logs.db adds auto_vacuum=INCREMENTAL
// before its first table, ADR 0015), applies the forward-only [Migration]
// list, opens a read pool of at most 10 connections and starts the writer
// goroutine.
//
// Every write is a job ([ApplyFunc]) sent with [Store.Submit] or
// [Store.Write]. The writer collects jobs until the batch holds 100 of them or
// 250 ms have passed since its first job, then runs them in one BEGIN
// IMMEDIATE transaction, each under its own savepoint, and commits. A job's
// outcome is sent only after the commit is durable, so an acknowledgement
// that follows it is never ahead of the disk. A job that fails is rolled back
// alone; a batch whose transaction breaks fails every job in it.
//
// Reads go through [Store.Read], a short read transaction on the pool that
// sees the last commit and never waits for the writer.
package store
