# internal/hub/ingest

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store` and `fleet-security`. Rules for this package:

- One Snapshot is one writer job: the core, every `Applier`, then ONLINE.
  Nothing marks a machine ONLINE outside that job (row 5 ruling, fork 1).
- An `Applier` uses only the job's `tx`: no network call, no wait, no
  `Commit`. It returns an error to roll the whole Snapshot back.
- Agent text is cleaned by `Clean` (or a later row's own step) on the ingest
  goroutine before the job, cut to a small bound before the scrubber
  (02e, 04b), never inside the transaction.
- No telemetry here: the gateway's Snapshot arm logs and counts the outcome
  `ApplySnapshot` returns.
- Tests use a real `state.db` in `t.TempDir()`; the gateway's tests drive
  the arm through the real listener.
- A change here updates this README and `doc.go` in the same commit.
