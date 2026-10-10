# internal/hub/outbox

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store`, `fleet-security` and `fleet-telemetry-coverage`. Rules
for this package:

- `Enqueue` only ever runs inside the caller's writer job; it never sends.
  Nothing else in the product puts a row in `commands`.
- No transaction spans a send: read pending rows through the read pool,
  queue them, then record the sends in a writer job. `ResultAck` is sent
  only after the result's job committed.
- `Run` is the only goroutine that sends commands. A command goes only to
  the stream of the epoch it was given (`Streams.Send` checks it), and a seq
  is used up only when the command was queued; never reuse one in an epoch.
- A send is not a state change: the row stays `pending` and its version and
  `hub_seq` do not move. Every Operation change bumps both.
- Agent free text is cut to its cap, stripped of control characters and
  scrubbed on the caller's goroutine, before the writer job; never inside
  it. Never log or label free text, a screen or a command body.
- A message that is not for a command of the stream's machine, or comes from
  a stream that is no longer live, writes nothing and is never answered.
- `Service.mu` guards only the ready and dirty sets; never hold it across a
  store call or a send.
- Tests use a real `state.db` in `t.TempDir()` and call `Enqueue` through the
  store's writer, the way product callers do.
- A change here updates this README, `doc.go` and 03f "The command path" in
  the same commit.
