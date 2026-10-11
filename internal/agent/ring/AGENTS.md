# internal/agent/ring

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-sqlite-store`. Rules for this package:

- `agent.db` has one writer: the `internal/store` it opens. Never open a
  second read-write connection to the file outside tests.
- Every write is a store job: no network call, no wait on another goroutine
  inside it. Read what a decision needs inside the same job.
- Migrations are forward-only: never edit one that has shipped; append the
  next. Each table cites the spec line it implements (02i, 03n W2, 04 Q9).
- Nothing in `agent_state` goes backwards, and a logged result never
  changes; a rule like that gets a trigger or constraint in the file, not
  only a check in Go, and a test that writes the file directly.
- Never log a command's result bytes; ids and counts only.
- `UnackedCount` (the heartbeat's `unacked_results`) is moved only by the
  after-func of a `writeThen`, after the batch committed. Any new write that
  finishes, acks or deletes a `command_log` row must move it the same way,
  with a test that compares it to `len(Unacked())`.
- A change here updates this README, `doc.go` and 02i's "What agent.db
  keeps" in the same commit.
