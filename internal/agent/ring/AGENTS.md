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
- A change here updates this README, `doc.go` and 02i's "What agent.db
  keeps" in the same commit.
