# Native session runtime

The agent's machine-side runtime for fleet-managed native sessions
(#266; [ADR 0033](../../../docs/adr/0033-native-sessions-and-handoff.md),
[02n](../../../docs/specs/02n-native-execution.md)). Five typed commands
(N1) arrive through the executor; this package's `Host` admits one journal
effect per command, claims it in per-session order, calls exactly one
qualified `Adapter` outside every store transaction, then resolves the
effect with bounded evidence.

## Layout

- `adapter.go`: the `Adapter` boundary (one qualified backend caller) and
  the classified event vocabulary. No shell string, path or credential
  crosses it; process spawns go through `internal/proc` in #267-#269.
- `host.go`, `effects.go`: admit, claim (with bounded order wait),
  resolve, orphan cleanup and startup recovery.
- `classify.go`: provider events to wire events (N2): scrubbed, capped,
  bounded output; unknown events recorded, never completed.
- `handler.go`: the five `exec.Handler`s. Every one is `NotRepeatSafe`:
  a restart ends a running native command `UNKNOWN_OUTCOME` and the
  journal holds the session for explicit reconciliation.
- `metrics.go`: terminal-effect and shed-byte counters, log lines.

Until a backend is qualified the adapter set is empty: the handlers still
register (the command kind exists), advertise nothing, and every native
command is answered `STATUS_UNSUPPORTED` naming `native.<backend>`.

## Invariants

One external effect per `command_id`. `PREPARED` authorizes nothing.
`MAY_HAVE_RUN` may have run: the answer is `UNKNOWN_OUTCOME`, never a
blind duplicate. Terminal states (`CONFIRMED`, `NO_EFFECT`) replay their
result. Exit, turn end, cleanup and goal verification stay distinct facts.
