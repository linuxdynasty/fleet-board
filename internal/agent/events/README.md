# internal/agent/events

The agent's event emitter: every event's seq, delivery class and
`coalesce_key`, the offline ring for the classes that need it, and the order
events go out on a stream
([02i](../../../docs/specs/02i-delivery-and-sequencing.md#queue-and-ring-rules-per-class),
[03n](../../../docs/specs/03n-reconnect.md#ordering-and-consistency)).

```go
e, err := events.New(ctx, events.Options{DB: db, Telemetry: tel}) // makes agent.db's one seq allocator
seq, err := e.Emit(ctx, ev)        // stamps, then: ring commit (NEVER_DROP, COALESCE) or shed queue (SHED)
c, err := e.Attach(ctx, helloAck.GetHubLastEventSeq(), snapshot.GetSeq()) // after the snapshot and unacked results
evs, more, err := c.Next(ctx, 64)  // in seq order; replayed = seq below the first snapshot's
err = c.Ack(ctx, eventAck.GetLastEventSeq()) // only when HelloAck listed event.ack
c.Detach()                          // stream ended: queued SHED events counted as shed
<-e.Ready()                         // something new for the attached cursor
```

| Class | Emit | While a stream is attached | No stream |
|---|---|---|---|
| NEVER_DROP | waits for the ring commit | sent from the ring in seq order | kept in the ring |
| COALESCE | waits for the ring commit; replaces the entry with its key (`coalesced_total`) | same | kept, one per key |
| SHED | never waits, never touches agent.db | bounded queue (256), oldest dropped first (`shed_total`) | dropped (`shed_total`) |

- **Order.** The seq is allocated and the ring job queued under one lock, so
  commits run in seq order (agent.db refuses an entry below the newest). The
  cursor's bound is the lowest seq still committing (or the next one), so a
  SHED event never goes out ahead of a lower NEVER_DROP one.
- **Caps.** One week or 64 MiB (defaults.md); the oldest entry is evicted
  first whatever its class, counted in `evicted_total` and logged
  `ring.evicted{n, oldest_seq}`: loss.
- **Trims are not loss.** At attach the entries at or below the HelloAck floor
  go (`ring.trimmed{reason="floor"}`); an `EventAck` drops what this stream
  sent at or below its seq (debug line, `reason="acked"`). Without `event.ack`
  in `HelloAck.hub_capabilities` the loop ignores an `EventAck` and entries
  stay until the next floor or the cap.
- **Counters.** `coalesced_total` and `evicted_total` move in the ring job's
  transaction; `shed_total` is saved by a background flush, and Hello and
  Heartbeat read all three from agent.db, so a counter the hub saw never
  falls after a crash.
- **Keys.** `<pane>` is `server/server_start_id/pane_id`; `<probe_id>`,
  `<account_key>`, `<harness>`, `<worktree path>`, `<tool>`, `<host>` come
  from the body's field of that name and must not be empty (`ErrNoKey`).
  `<profile>` is `HarnessInfo.profile`, empty for the default profile and
  always empty for `account_identity`; it is the one part that may be empty
  (`harness:claude:`, `harness:claude:work`; ruling round 3, 3a).
- **Oversize.** An event whose encoding is above `protoversion.MaxPartBytes`
  (960 KiB, the snapshot part cap of 02e) could never be sent, so `Emit`
  refuses it with `ErrOversize` before it takes a seq: a NEVER_DROP or
  COALESCE one counts as loss (`evicted_total` + 1, so the hub asks for a
  snapshot), a SHED one as shed; `ring.emit_refused{kind, bytes}` at most
  once per kind per minute. A stored entry above the cap is skipped, deleted
  and counted as loss in one job (`ring.unreadable{err_kind="oversize"}`),
  so it can never poison a stream.

Signals (03n "Telemetry"): `fleet_agent_events_total{kind, delivery}`,
`fleet_agent_events_shed_total`, `_events_coalesced_total`,
`_events_evicted_total`, `fleet_agent_replayed_events_total`,
`fleet_agent_ring_dropped_total{reason}`,
`fleet_agent_events_refused_total{reason="oversize"}`, gauges `fleet_agent_ring_events`
and `fleet_agent_ring_bytes`; span `agent.ring.replay`; logs `ring.evicted`,
`ring.trimmed`, `ring.replay`, `ring.unreadable`, `ring.emit_refused`,
`ring.emit_failed`, `ring.counters_failed`.

Tests use a real agent.db in `t.TempDir()`: a NEVER_DROP event replayed after
a crash before any stream, SHED counted and never kept, the shed queue's
oldest-first drop, COALESCE replacement, floor and ack trims (an ack above
what was sent keeps the unsent entry), eviction counted and logged, a SHED
event held behind a lower seq still committing, 200 concurrent producers
arriving once each in rising seq order, an over-cap event refused and counted
(and its log line limited), and an over-cap stored entry skipped, deleted and
counted once, under `-race`.
