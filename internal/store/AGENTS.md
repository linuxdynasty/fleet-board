# internal/store

Read the root `AGENTS.md` first; it applies here. Load `fleet-sqlite-store`
(and `fleet-telemetry-coverage` for a signal change). Rules for this package:

- One writer per file. Nothing outside the writer goroutine opens a write
  transaction; new write paths are jobs sent with `Submit` or `Write`.
- Never weaken a pragma (ADR 0014) to make a test or benchmark faster. The
  pragma tests read the values back from real connections and from the file.
- A job never spans a network call and never waits on another goroutine.
- Outcomes are sent after the commit, never before: an ack that follows a
  job's outcome must be durable.
- Migrations are forward-only and never edited after they ship; the checksum
  check refuses an edited one. Tables belong to the rows that need them, not
  to this package.
- Tests use real SQLite files in `t.TempDir()`, run with `-race`, and observe
  behavior through `Open`, `Submit`, `Write`, `Read`, a reopen, the file
  itself, or a metrics scrape; never through private fields.
- `modernc.org/sqlite` only (ADR 0014, 0020); no cgo.
- A change here updates this README and `doc.go` in the same commit, and 03f
  or 04 when the writer contract changes.
