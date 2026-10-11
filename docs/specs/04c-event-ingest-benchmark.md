# 04c. Event ingest: per-message jobs at the replay burst

- Status: In progress (plan row 5 PR 5c2); the gate is not settled yet
- Date: 2026-10-11
- Part of: [README](README.md), step 4; follows [04](04-technology-review.md)
  Q7 (replay budget). Required by the row 5 ruling, fork 6.

The hub stores each `Event` as one writer job (row 5 ruling, fork 6): the
history row, `hub_last_event_seq` and the Watch stamp (`hub_counters`
UPDATE) in the job's savepoint, many jobs per commit. Q7's S1 numbers came
from a shim that batched raw inserts with no savepoint per job, so they do
not show whether this shape keeps up. This page measures it.

## Budget and switch rule

At the S1 burst shape (2,500 events/s for 60 s, 300-byte events, one
machine stream, `synchronous=FULL`, `fullfsync`, the `hub_seq` UPDATE in
every job):

| Measure | Budget |
|---|---|
| Jobs committed per second, sustained | at least 2,500 |
| Commit (one batch) p99 | at most 50 ms |
| Enqueue to commit p99 | at most 60 ms |
| Writer busy (batch time / wall time) | at most 70 % |

Any miss switches to per-machine micro-batch jobs of at most 50
consecutive events of one stream, in arrival order, falling back to
per-message jobs on any failure; that variant needs its own numbers before
it lands. Never traded: one writer, ack after commit, arrival order per
machine.

## Method

- `BenchmarkEventBurst` in `internal/hub/ingest/burst_bench_test.go`: a real
  `state.db` made by `statedb.Create` (every migration, the production
  pragmas) in a temporary folder, one machine with a live connection and an
  applied Snapshot, then a producer that offers exactly 2,500 events/s
  (what is due since the start, every 10 ms) through `ingest.EventIn` and
  `ingest.SubmitEvent`, never waiting for a commit, as the gateway's Event
  arm does. The event is a replayed NEVER_DROP `harness_condition` padded
  to 300 bytes on the wire, so every job writes a history row. One
  iteration is one 60 s run:

  ```bash
  FLEET_EVENT_BURST=1 CGO_ENABLED=0 go test -run '^$' -bench EventBurst \
    -benchtime=1x -count=10 ./internal/hub/ingest/ | tee new.txt
  benchstat new.txt
  ```

  Reported: `jobs/s` (committed over the offered window), `commit_p99_ms`
  (from the store's `fleet_hub_writer_batch_seconds` buckets; 50 ms is a
  bucket bound, so "at most 50 ms" is exact), `enqueue_commit_p50_ms` and
  `_p99_ms` (Submit to the job's result), `busy_pct`, `jobs/batch`,
  `drain_ms` (last commit after the producer stopped) and `failed`.
- The S1 shim the same day on the same machine:
  `cd spikes/sqlite && CGO_ENABLED=1 go build -o s1 . && ./s1 -driver modernc -scenario burst -dur 60s`
  (its disk counter needs cgo on macOS; it is throwaway spike code).
- Machine: Apple M4 Pro, 12 cores, 64 GB, macOS 27.0.1, APFS on the
  internal SSD; Go 1.27.2 darwin/arm64; base commit 9cb5d13 plus this PR.

## Results

### Preliminary, busy host (2026-10-11)

Other agents were building and testing on the machine (load average 5 to
21), so these runs say nothing about the budget; they are kept to show the
spread. Back to back:

| Run | Load | Jobs/s | Commit p99 | Enqueue to commit p50 / p99 | Busy |
|---|---|---|---|---|---|
| S1 shim, burst (Q7 recorded: 2,626 rows/s, 28 ms, 30 / 58 ms) | 5 to 10 | 2,604 | 70 ms | 31 / 311 ms | not reported |
| `BenchmarkEventBurst`, 60 s | 10 to 14 | 2,489 | 98 ms | 32 / 231 ms | 42 % |
| `BenchmarkEventBurst`, 60 s, earlier | 21 | 928 | 3,190 ms | 272 / 10,048 ms | 93 % |

The unchanged S1 shim went from 28 ms to 70 ms commit p99 on this host
today, so the tails follow the host's load, not the job shape. Writer CPU
is not the limit (42 % busy at 2,500 jobs/s, 100 jobs per batch).

### Quiet host

Not run yet: needs the machine to itself for about 25 minutes (ten 60 s
runs plus the S1 rerun).
