# internal/hub/approvals

The hub's owner decisions: answering a harness permission prompt,
confirming a destructive command or a `KillWindow` on a developer's window,
or confirming a dispatch with a `CONFIRM` check (03d W7, 02m).

- `CreateApproval` stores one `PENDING` approval (version 1) in the raising
  writer job's transaction and stamps it for Watch.
- `Decide` records the decision and audits `approval.decide`. Checks run in
  order: the `request_id` replay, the state (`ErrClosed` wins over
  `ErrVersion`), the version, the role, then the decision. An approve
  decision for a prompt reserves the in-flight row; the caller enqueues the
  command with `outbox.Enqueue` and records it with `AttachOperation` in the
  same transaction. A deny sends nothing.
- `ExpireDue` (and the lazy step in `Get` and `Decide`) moves past-due
  `PENDING` approvals to `EXPIRED` with the `DENY` fallback, audited as
  `approval.expire`. The housekeeping job runs the pass every minute.
- `Withdraw` retires a prompt approval whose prompt left the screen
  (`PromptGone`, pane exit), audited as `approval.withdraw`; a later
  decision fails closed. Row 5's ingest calls it from the event's
  transaction.
- `ClearInflight` drops the prompt's answer row when its `CommandResult`
  lands; row 5's result path calls it.

Tables live in `state.db` (migration 12 in `internal/hub/statedb`):
`approvals`, one row per decision, and `approval_inflight`, one row per
prompt with an answer on its way.

Tests: `CGO_ENABLED=0 go test ./internal/hub/approvals/`.
