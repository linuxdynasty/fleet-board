# Commit wake contract

RULING (arbiter-fleet, #40): `Store.CommitWake()` exposes a Store-owned,
capacity-one channel. One future hub Watch runtime consumes it. After a
successful outer `COMMIT`, the writer sends a nonblocking wake before sending
buffered job outcomes. A wake has no payload; the consumer reads committed
state to learn what changed. A failed outer commit sends no wake. A committed
batch may wake even when every job made no change or returned an error. The
writer closes the channel after its final flush during shutdown.

```text
callers -> bounded jobs queue -> single writer
                              -> BEGIN IMMEDIATE
                                 -> per-job savepoint, resource/stamp/log changes
                              -> outer COMMIT
                              -> capacity-one wake (nonblocking)
                              -> buffered job outcomes
                                        |
                                        v
                         future hub Watch runtime: sole ordered dispatcher
```

The writer owns the send and close, so they cannot race. A full wake channel
coalesces commits; the future consumer must read committed state rather than
count wakes. `Store.mu` still guards only closure and submission to the jobs
queue. No SQL read, network call, wait, or extra lock occurs while notifying.
The transaction ends before notification, and no mutex spans the transaction.
Callers whose context ends after enqueue can stop waiting; their job still
commits or fails and a successful batch still wakes the consumer. During
`Close`, queued jobs are flushed before the wake channel closes.
