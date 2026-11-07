# logs.db store

The hub's session-log store (row 25, [03d](../../docs/specs/03d-triage-sessions-logs.md)
W9, [ADR 0015](../../docs/adr/0015-session-logs-in-sqlite.md)): the
`logs.db` writer, its retention, and the reads behind it.

`logs.db` holds `log_sessions` (one row per session), `log_chunks` (one row
per chunk the agent ships: chunk and index in one row, so a crash leaves
either the whole chunk or none of it) and `archived_pane_summaries` (the
last pane summary of each pane, moved here when its session ends). It has
its own single writer through `internal/store` named `logs` (same pragmas
as `state.db` plus `auto_vacuum=INCREMENTAL`); one transaction per call,
which the store batches.

Sessions are keyed by the hub `SessionId`. The gateway resolves the agent
wire's `(pane, stream)` to the session before calling `Ingest`; the
`Resolver` seam is that map, injected so this package never reads
`state.db`.

The four reads (`Read`, `List`, `Get`, `Search`), plus `Stat` and
`Delete`, are built for the future `FleetService` calls of the same names;
until those land nothing calls them in production. Those calls will enforce
the `LOGS` scope and audit the delete; this package does neither.
