# internal/agent/exec

The agent's command executor (plan row 6, PR 6e). It answers every
`Command` the hub sends with one `CommandResult`, after the envelope
checks of [02, "Envelope checks"](../../../docs/specs/02-contracts.md#delivery-semantics),
and never types into a pane itself: that is a handler's job. The current
production run registers only default-server `Interrupt` (F4).

```go
ex, err := exec.New(exec.Options{
	DB: db, Sender: loop, Clock: loop.HubClock(), Policy: settingsManager,
	Audit: auditLog, Home: home, Telemetry: tel, Handlers: handlers,
})
go ex.Run(ctx)
// LoopOptions: Command: ex.Command, OnConnected: ex.OnConnected, Capabilities: ex.Capabilities
```

The production wiring also passes `settingsManager.Hook` as the connection's
Settings hook and runs that manager before connecting. F4's handler checks the
effective default-server mode again when it executes or resumes. With no local
approver yet, a required approval waits until `LOCAL_CONFIRM_TIMEOUT`.

## Order of checks

1. **Known `command_id`**: answered from the log, never checked or run
   again: the logged result, or another `CommandAck` while it runs.
2. **Deadline** on the strict hub clock (`conn.HubClock`); a hub time the
   agent cannot read is past. `EXPIRED` is logged and acked, its seq not
   applied (the epoch check comes after).
3. **Epoch** and 4. **seq**, with the log row, in one store job
   (`ring.LogAndApply`). `EPOCH_MISMATCH` (retryable) and `SEQ_REPLAYED`
   are answered unlogged and unacked. Then `CommandAck`.
5. **Requires**: a handler for the kind and some handler advertising the
   capability at that nonzero version, else `UNSUPPORTED` with
   `missing_capability`.
6. **Policy**: safe mode lets only observe commands through
   (`POLICY_SAFE_MODE`); the rate limit (`RATE_LIMITED`, retryable,
   `retry_after` when the window frees).
7. **Role**: the 02g matrix (`roles.go`, compared cell for cell with the
   spec by a test); `ROLE_INSUFFICIENT`. For `KillWindow` the row comes
   from the kind's handler, which reads the fleet's tags live on the
   command context (the safe-mode check in step 6 places the window the
   same way); a lookup the context cancels, or that tmux fails, reads
   the developer's window, the strictest row.
8. **Approval**: an `L!` cell, or a `local_approval` rule for the kind,
   sends `AWAITING_LOCAL_CONFIRM` and waits for the `Approver` until the
   deadline: `LOCAL_DENIED` or `LOCAL_CONFIRM_TIMEOUT`. With no approver
   (until rows 18, 20, 24) a wait ends at the deadline.

Rules the 6e ruling settled, each stated in its spec: an observe command,
the only kind safe mode runs, is one the 02g VIEWER column allows
(`CapturePane`, OBSERVE `RunHarnessCommand`, read-only `OpenAttach`,
`CloseAttach`), whatever the role; `ARBITER` has no `Role` value, so its
column is never reached and an unknown role is refused; a `local_approval`
rule makes every allowed cell of its kind wait, `A` cells included; `F`
and `F*` are the handler's to judge, "after `merged_grace`" is the hub's
timer, and a `KillWindow` the handler cannot place is a developer's
window (02g legend); free text is cut back to whole lines within 8 KiB before the scrub
(`scrub.Bound`; empty when its first 8 KiB hold no line end). The rate window is rebuilt
from the last minute of the command log at start (`ring.Recent`), so a
restart does not reset it; a read that fails is logged
(`command.store_failed{step=window}`) and the window starts empty.

## Concurrency

```text
 conn sending goroutine            envelope goroutine (Run)                 command goroutines (one per command)
 ─────────────────────            ────────────────────────                 ─────────────────────────────────────
 Command hook ──non-blocking──▶ intake chan (64) ─▶ known id? (read)
   full: false, conn drops it                       deadline (strict clock)
 OnConnected ─non-blocking─▶ resume chan (1) ─┐     LogAndApply ── store job: insert + seq advance (one tx)
                                              │     rate window (only this goroutine)
                                              │     Send(CommandAck)
                                              │     decide: requires, policy, role
                                              │     claim(id) ── mu ──▶ running set
                                              │     wg.Add, go do() ─────────────────▶ approval wait (strict clock, Approver)
                                              └──▶ each: resumeOpen (Open read, claim,  SetRunning   ── store job
                                                   re-read, go do())                    handler.Run / Resume
                                                   each: resend last progress            RecordResult ── store job, then
                                                         (snapshot of running set        Send(CommandResult)
                                                          under mu, Send outside it)    release(id) ── mu
```

- **Shared state**: the running set (`map[command_id]*run`, the last
  progress of each) under one mutex, held only for map reads and writes,
  never across a store job or a `Send`. The rate window
  and the registry belong to the envelope goroutine alone.
- **Decide may read tmux**: the role and safe-mode checks ask the kind's
  handler to place the command, and that answer may read tmux on the
  command context. It runs on the envelope goroutine (live commands and
  the resume pass alike), holding no mutex and doing no store job, so a
  slow lookup only delays the commands behind it; a cancelled context
  places nothing.
- **Channels**: the intake (64) and the resume signal (1) are written
  without blocking by hooks on the connection's sending goroutine.
- **Store**: every write is one `agent.db` job; none spans a network call.
  The log row and the seq advance share one job; a result is committed
  before it is queued; `CommandAck` follows the commit.
- **Sends** use the run's context, never a hook's, so a full queue waits
  instead of failing; `ErrNoStream` is normal (results are re-sent from the
  log after the next snapshot, acks re-derived by the hub's re-send,
  progress re-sent on the next `OnConnected`).
- **Stop**: `Run` returns after every command goroutine (`wg.Wait`), so
  `Handler.Run`, `Handler.Resume` and `Approver.Await` must return soon
  after their context ends, or they hold the agent's shutdown. A run cut
  off leaves its entry received or running for the resume pass at the
  next connect, as does a `SetRunning` or `RecordResult` that failed.

`RefusedTotal()` is how many commands the executor's own checks refused
since start, logged or not; the heartbeat's
`AgentHealth.commands_refused_total` reads it (wired in 6i).

Signals: logs `command.received`, `command.refused`, `command.finished`,
`command.resumed`, `command.dropped`, `command.store_failed`,
`command.send_failed`, `command.text_emptied`; metrics
`fleet_agent_commands_total{kind, status}`,
`fleet_agent_command_seconds{kind}`, `fleet_agent_commands_running`,
`fleet_agent_commands_refused_total{reason}`,
`fleet_agent_command_store_failed_total{step}`,
`fleet_agent_text_scrub_total{field, result}`; span `agent.command{kind}`
([05b](../../../docs/specs/05b-observability.md)).

Tests: every kind arm, an empty kind and field 99 are `UNSUPPORTED` with no
handler run; one test per refusal path (which also writes the scrub corpus
with `FLEET_EXEC_CORPUS_OUT`); ack then result, the result sent equal to
the logged one; a known id never run twice; the sleep acceptance line; the
approval wait re-sent on connect; restart recovery per class; the role
matrix against 02g; and that nothing here imports `internal/agent/tmux`.
