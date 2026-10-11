# internal/agent/exec

The agent's command executor (plan row 6, PR 6e). It answers every
`Command` the hub sends with one `CommandResult`, after the envelope
checks of [02, "Envelope checks"](../../../docs/specs/02-contracts.md#delivery-semantics),
and never types into a pane itself: that is a handler's job, and row 6
registers none.

```go
ex, err := exec.New(exec.Options{
	DB: db, Sender: loop, Clock: loop.HubClock(), Policy: exec.FromPolicy(pm),
	Audit: auditLog, Home: home, Telemetry: tel, Handlers: exec.Handlers(),
})
go ex.Run(ctx)
// LoopOptions: Command: ex.Command, OnConnected: ex.OnConnected, Capabilities: ex.Capabilities
```

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
   capability at that version, else `UNSUPPORTED` with
   `missing_capability`.
6. **Policy**: safe mode lets only observe commands through
   (`POLICY_SAFE_MODE`); the rate limit (`RATE_LIMITED`, retryable,
   `retry_after` when the window frees).
7. **Role**: the 02g matrix (`roles.go`, compared cell for cell with the
   spec by a test); `ROLE_INSUFFICIENT`.
8. **Approval**: an `L!` cell, or a `local_approval` rule for the kind,
   sends `AWAITING_LOCAL_CONFIRM` and waits for the `Approver` until the
   deadline: `LOCAL_DENIED` or `LOCAL_CONFIRM_TIMEOUT`. With no approver
   (until rows 18, 20, 24) a wait ends at the deadline.

Choices this package makes where the spec is silent (named in the PR):
an observe command, the only kind safe mode runs, is one the matrix lets a
`VIEWER` ask for; `ARBITER` has no `Role` value, so its column is never
reached and an unknown role is refused; `EnsureScratch` and
`CollectScratch` have no matrix row and are refused; the VIEWER cell
"A (read-only)" of `OpenAttach`, `CloseAttach` limits `OpenAttach` to
`read_only` and lets a viewer close an attach; a `local_approval`
rule makes any allowed cell wait, never loosens one; F and F* conditions
are handed to the handler; free text is cut to 8 KiB before the scrub; the
rate window lives in memory and starts empty at each start.

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
                                              └──▶ first: resumeOpen (Open read,        SetRunning   ── store job
                                                   claim, go do())                      handler.Run / Resume
                                                   each: resend last progress            RecordResult ── store job, then
                                                         (snapshot of running set        Send(CommandResult)
                                                          under mu, Send outside it)    release(id) ── mu
```

- **Shared state**: the running set (`map[command_id]*run`, the last
  progress of each) under one mutex, held only for map reads and writes,
  never across a store job or a `Send`. The rate window, the resumed flag
  and the registry belong to the envelope goroutine alone.
- **Channels**: the intake (64) and the resume signal (1) are written
  without blocking by hooks on the connection's sending goroutine.
- **Store**: every write is one `agent.db` job; none spans a network call.
  The log row and the seq advance share one job; a result is committed
  before it is queued; `CommandAck` follows the commit.
- **Sends** use the run's context, never a hook's, so a full queue waits
  instead of failing; `ErrNoStream` is normal (results are re-sent from the
  log after the next snapshot, acks re-derived by the hub's re-send,
  progress re-sent on the next `OnConnected`).
- **Stop**: `Run` returns after every command goroutine; a run cut off
  leaves its entry received or running for the next start's resume.

Signals: logs `command.received`, `command.refused`, `command.finished`,
`command.resumed`, `command.dropped`, `command.store_failed`,
`command.send_failed`; metrics `fleet_agent_commands_total{kind, status}`,
`fleet_agent_command_seconds{kind}`, `fleet_agent_commands_running`,
`fleet_agent_commands_refused_total{reason}`; span `agent.command{kind}`
([05b](../../../docs/specs/05b-observability.md)).

Tests: every kind arm, an empty kind and field 99 are `UNSUPPORTED` with no
handler run; one test per refusal path (which also writes the scrub corpus
with `FLEET_EXEC_CORPUS_OUT`); ack then result, the result sent equal to
the logged one; a known id never run twice; the sleep acceptance line; the
approval wait re-sent on connect; restart recovery per class; the role
matrix against 02g; and that nothing here imports `internal/agent/tmux`.
