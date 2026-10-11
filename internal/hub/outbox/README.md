# internal/hub/outbox

The hub's command path: the outbox, the `Operation` each command is, and the
agent's answers. Spec: [03f "The command path"](../../../docs/specs/03f-hub-internals.md#the-command-path)
and [02i "Command lifecycle"](../../../docs/specs/02i-delivery-and-sequencing.md#command-lifecycle).
Plan row 5, PR 5f. Expiry, revoke and pruning come with PR 5g.

## API

| Call | Where it runs | What it does |
|---|---|---|
| `Enqueue(ctx, tx, In)` | inside the caller's writer job | stores one pending row (Operation `PENDING`, version 1, a `hub_seq` stamp); a known `command_id` returns the stored row with `Created` false; mints a `command_id` when the Command has none |
| `Service.Notify(ops...)` | after that job committed | logs `outbox.enqueued` and wakes the sender for the machines |
| `Service.Ready(machine, epoch)` | after the Snapshot of that epoch committed (the gateway's `ready` seam) | the machine's pending commands may go out on that epoch |
| `Service.Gone(machine, epoch)` | when that epoch's stream ended | stops sending on it |
| `Service.Run(ctx)` | one goroutine, owned by the gateway's `Server` | the sender |
| `Service.Ack`, `Progress`, `Result` | the stream's ingest goroutine | clean the free text, decide on the read pool, then apply a change in one writer job; a result the agent did not log (`EPOCH_MISMATCH`, `SEQ_REPLAYED`) is refused and the command goes out again under the live epoch |

## Concurrency

```text
 caller (API, scheduler)        stream ingest goroutine (per stream)       Service.Run (one)
 writer job: decision rows      CommandAck/Progress/Result arrives         wake <- Notify/Ready/Ack
   + Enqueue(tx)  ---commit-->  cut to 1 KiB, strip, scrub (no tx)        take(): ready ∩ dirty (mu, map work only)
 Notify(op) -> kick (mu)        writer job: load row, change it            read pool: pending rows, max seq of epoch
                                  (one savepoint, no network)              Streams.Send per row (non-blocking,
                                after commit: ResultAck -> stream queue      registry lock for the lookup only)
                                Ack -> kick (mu)                           one writer job per pass: record the sends
```

- Locks: `Service.mu` (ready and dirty sets) and the gateway registry's
  mutex (map lookup) are each held for map work only, never together, never
  across a store call or a send. No lock order is needed.
- Transactions: the caller's decision job holds `Enqueue`; each answer that
  changes a row is one job (a repeat or a refusal is decided on the read
  pool and writes nothing); each sender pass records all its sends in one
  job, after the sends. None spans a network call. Each answer still waits
  for its own batch window on the ingest goroutine; plan row 5c2's fork-6
  benchmark covers the command arms and the sender (03f "Not yet").
- Paging: the sender reads pending rows a page at a time after the last
  row id it saw, so rows it skips (already sent on the epoch, undecodable)
  never hide the rows behind them.
- Races and their backstops: an ack can commit before its send is recorded
  (the agent answered within the batch window), so a send is recorded only
  over an older epoch's and an answer needs only a live stream of the right
  machine. A displaced stream's queued answers are refused (`stale_stream`);
  the agent sends them again on its live stream. `commands.command_id` is
  unique; `(machine_id, sent_epoch, sent_seq)` is unique; the in-memory set
  of what an epoch was sent keeps a send whose record failed from going
  twice.

## Cleaning agent text

`CommandResult.message` and `CommandProgress.message` are cut to 1 KiB on a
UTF-8 boundary before anything reads them whole (04b: unbounded, 1 MiB takes
the scrubber about 16 s), stripped of control characters but `\n` and `\t`
(02e), cut again (each invalid byte became a 3-byte U+FFFD), then passed
through `scrub.Scrub` with no home folder. The step name is cut to 256 bytes
and stripped, not scrubbed. A result's metadata keeps its first 16 keys in
sorted order, each key and value cut to 256 bytes without control
characters. Its `detail` is kept as the agent sent it (bounding it is noted
for plan row 5g).

## Telemetry

Listed in 03f "The command path": `fleet_hub_commands_total{kind,status}`,
`fleet_hub_command_roundtrip_seconds{kind}`,
`fleet_hub_command_messages_total{arm,outcome}`,
`fleet_hub_results_unknown_command_total`, `fleet_hub_outbox_sends_total{result}`,
`fleet_hub_outbox_pending{machine}`, `fleet_hub_outbox_oldest_age_seconds{machine}`,
`fleet_hub_scrub_total{field,result}`, `fleet_hub_scrub_oversize_total{field}`;
logs `outbox.enqueued`, `outbox.sent`, `outbox.acked`, `outbox.resulted`,
`outbox.refused`, `outbox.failed`; spans `hub.command.send`, `hub.command.result`.

## Tests

`CGO_ENABLED=0 go test -race ./internal/hub/outbox/`. The gateway's
`commands_test.go` runs the round trip through the real listener.
