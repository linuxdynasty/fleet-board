# internal/agent/exec

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`.
Rules for this package:

- Keep the check order of 02-contracts "Envelope checks". The log row and
  the seq advance stay one store job (`ring.LogAndApply`), after the
  deadline; never apply a seq without its log row.
- `EPOCH_MISMATCH` and `SEQ_REPLAYED` are never logged or acked; every
  other refusal is logged, acked and answered once.
- Deadlines use the strict `conn.HubClock` only; a hub time the agent
  cannot read is past.
- A result is committed to the command log before it is queued; send with
  the run's context, never a hook's. Hooks (`Command`, `OnConnected`) only
  hand over without blocking.
- Never import `internal/agent/tmux` or anything below it (a test checks):
  only a handler, registered by a later row, reaches a pane, and a
  handler's capability is advertised only from the row that builds it.
- `roles.go` is 02g's matrix verbatim; change both together (a test
  compares them).
- Free text leaves only through `text` (scrub, home as `~`, 1 KiB). Logs
  and audit rows carry ids, kinds and enum names, never a message.
- A change here updates this README, `doc.go`, 02-contracts, 02i and 05b
  in the same commit.
