// Package logstore is the hub's session-log store (row 25, 03d W9, ADR
// 0015): the logs.db writer, its retention, and the reads behind it.
//
// logs.db holds log_sessions (one row per session), log_chunks (one row per
// chunk the agent ships, chunk and index in one row so a crash leaves either
// the whole chunk or none of it) and archived_pane_summaries (the last pane
// summary of each pane, moved here when its session ends). It has its own
// single writer through [store.Store] named "logs" (same pragmas as
// state.db plus auto_vacuum=INCREMENTAL); one transaction per call, which
// the store batches. LogAck answers only after the commit.
//
// Sessions are keyed by the hub SessionId the caller passes. On the agent
// wire a stream is (pane, stream) and the session id is optional; the
// gateway resolves the pane to the session it assigned and fills it in
// before calling [Store.Ingest]. [Resolver] is that mapping, injected so
// this package never reads state.db.
//
// The four reads (Read, List, Get, Search), plus Stat and Delete, serve the
// FleetService calls of the same names; those calls enforce the LOGS scope
// and audit the delete, this package does neither.
package logstore
