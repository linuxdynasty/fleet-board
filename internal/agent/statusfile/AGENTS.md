# internal/agent/statusfile

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- The files hold closed words, times, numbers and hub addresses. Never a
  token, an error string, a hub message or a path. New text fields go through
  `enum`, `token` or `addr` (model.go); `FromConn` and `Read` both apply them.
- A value not yet known is left out of the file, never written as 0 or an
  empty word. A new field is a pointer or `omitempty`, and a test shows it
  absent when unset. A new running-agent fact adds its entry here and its test
  in the row that adds it (plan row 6).
- Every write is `writeAtomic`: temp file, fsync, rename, folder fsync, mode
  0600. One writer per file: `fleet-agent run` for `status.json`, `fleet-agent
  enroll` for `enroll.json`. A failed write is logged and counted, never fatal.
- No timer except the spec'd 60 s floor (defaults.md) re-reads the loop's
  state. The 1 s throttle only spaces writes; a change reaches the writer
  through `Notify` or `Options.Changed` (ADR 0022 rule 6, ADR 0024 decision
  9). Do not add a ticker that calls the source.
- `Notify` never blocks. `Stop` uses no context, so a signal cannot cut the
  last write, and it waits at most 5 s for it (`status.stop_timeout`), so a
  hung disk cannot hold up the exit. One goroutine does all file work.
- `enroll.json` is the last failure of a machine that is not enrolled yet:
  a refused re-enroll of an enrolled machine neither writes nor clears it,
  `run` removes it, and the state folder is never made for it.
- The order of a write is write, fsync, rename, folder fsync, and
  `TestWriteOrderIsWriteFsyncRenameDirFsync` holds it. Keep the `fileOps` seam.
- `Read` is lenient about unknown keys (advisory file) and strict about the
  layout `version`. This is the opposite of `conn.CheckEnded`, which gates a run.
- Tests drive the writer with the fake clock in `writer_test.go`: no sleeps.
- A change here updates this README, `doc.go`, 03 W1 (Persistence and
  Telemetry) and plan row 6 in the same commit.
