# internal/agent/events

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules for this package:

- `delivery_table.go` is written by `scripts/gen-doc-tables.py --write` from
  the `[DELIVERY]` tags in `api/fleet/agent/v1/events.proto`; never edit it by
  hand, and never give it a `Code generated ... DO NOT EDIT.` header (lint
  must still read it; row 7 ruling fork 3).
- A seq is allocated and its ring job queued under `Emitter.mu`, so ring
  commits run in seq order; the agent.db file refuses an entry below the
  newest. Never hold `mu` across a commit wait or a send.
- The cursor never sends an event while a lower seq is still being
  committed. An `EventAck` trims only what this stream sent. Keep the tests
  that prove both.
- Counters on the wire come from agent.db only: a shed drop is counted in
  memory and saved by the flush goroutine before Hello or Heartbeat can
  report it. A counter the hub saw must never fall after a crash.
- Every key placeholder but `<profile>` must be non-empty (`ErrNoKey`);
  `<profile>` empty is the default profile (ruling round 3, 3a). Never fill a
  key with a guess.
- An event above `protoversion.MaxPartBytes` is never stored or sent: refuse
  it at emit and count it (evicted or shed); a stored one is deleted and
  counted once. A poison entry would end every stream that replays it.
- Never put event body text in a log line or a label: seqs, counts, kinds.
- A change here updates this README, `doc.go`, and 02i or 03n in the same
  commit.
