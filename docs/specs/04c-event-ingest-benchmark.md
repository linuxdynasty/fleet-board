# 04c. Event ingest: per-message jobs at the replay burst

- Status: Measured 2026-10-11 (plan row 5 PR 5c2); accepted, arbiter round 4: per-message jobs stay; the enqueue-to-commit line is 90 ms (derived below)
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
machine stream, synchronous=FULL, fullfsync, the `hub_seq` UPDATE in
every job):

| Measure | Budget |
|---|---|
| Jobs committed per second, sustained | at least 2,500 |
| Commit (one batch) p99 | at most 50 ms |
| Enqueue to commit p99 | at most 90 ms at 2,500 events/s: the batch fill at the rate (100 jobs / 2,500 per s = 40 ms, batch size from ADR 0014) plus the commit p99 budget (50 ms). Arbiter round 4; supersedes the row-5 fork-6 line of 60 ms, which was the S1 shim's measured 59 ms plus 1 ms, less than the shim's own spread and drift (57.6-58.9 ms in Q7, 59.5 ms today) |
| Writer busy (batch time / wall time) | at most 70 % |
| Jobs in flight per stream at p99 (enqueue-to-commit p99 x the rate) | under the arm's cap of 256 ([defaults](defaults.md), "Event jobs in flight per stream") with margin |

The producer offers exactly 2,500 events/s, so "sustained" is read as: no
job failed, every submitted job committed, and the last ones committed
within about one enqueue-to-commit p99 of the producer stopping
(`drain_ms`): the writer kept up. A job also waits for its batch to fill
(100 jobs, about 40 ms at this rate) before the commit starts, so
enqueue to commit is about that wait plus the commit.

A miss of jobs per second, commit p99 or writer busy switches to
per-machine micro-batch jobs of at most 50 consecutive events of one
stream, in arrival order, falling back to per-message jobs on any failure;
that variant needs its own numbers before it lands. A miss of the
enqueue-to-commit line cannot be fixed by micro-batching: at <= 50 events
per job the writer sees <= 50 jobs/s, 100 jobs no longer arrive inside the
250 ms window, and every event waits for the window (about 250 ms plus the
commit, as the drain and the S1 steady rows show). Never traded: one writer, ack after commit, arrival order per
machine.

## Method

- BenchmarkEventBurst in `internal/hub/ingest/burst_bench_test.go`: a real
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
- The S1 shim the same day on the same machine (its disk counter needs
  cgo on macOS; it is throwaway spike code):

  ```bash
  cd spikes/sqlite && CGO_ENABLED=1 go build -o s1 . && ./s1 -driver modernc -scenario burst -dur 60s
  ```
- Machine: Apple M4 Pro, 12 cores, 64 GB, macOS 27.0.1, APFS on the
  internal SSD; Go 1.27.2 darwin/arm64. The quiet-host runs were measured at ed6baed, before the rebases onto b4ae10f and a5dbb9e. At 2a4a1c9 the per-event job is unchanged: internal/store, internal/hub/ingest/event.go and clean_event.go are identical, and internal/hub/statedb/events.go differs only in a comment (the events table is now migration 11, after row 5g's migration 10). The other changes in those packages since are off the measured path: 5g's command expiry in the Snapshot job (used only to set up the benchmark) and its command indexes, the RecordEventLoss bound check, the benchmark comment and tests.

## Results

### Preliminary, busy host (2026-10-11)

Other agents were building and testing on the machine (load average 5 to
21), so these runs say nothing about the budget; they are kept to show the
spread. Back to back:

| Run | Load | Jobs/s | Commit p99 | Enqueue to commit p50 / p99 | Busy |
|---|---|---|---|---|---|
| S1 shim, burst (Q7 recorded: 2,626 rows/s, 28 ms, 30 / 58 ms) | 5 to 10 | 2,604 | 70 ms | 31 / 311 ms | not reported |
| BenchmarkEventBurst, 60 s | 10 to 14 | 2,489 | 98 ms | 32 / 231 ms | 42 % |
| BenchmarkEventBurst, 60 s, earlier | 21 | 928 | 3,190 ms | 272 / 10,048 ms | 93 % |

The unchanged S1 shim went from 28 ms to 70 ms commit p99 on this host
today, so the tails follow the host's load, not the job shape. Writer CPU
is not the limit (42 % busy at 2,500 jobs/s, 100 jobs per batch).

### Quiet host (2026-10-11)

Other developers idle and no builds or tests but these; load average 2.97
before, 2.12 after the ten runs and 1.34 after the S1 rerun (it rose to 7
during the runs: macOS counts threads waiting on fsync, and no other build
ran; the owner's editor was the only other busy process). Ten 60 s runs,
`benchstat` (median ± spread):

| Measure | Budget | BenchmarkEventBurst | Verdict |
|---|---|---|---|
| Jobs committed per second | at least 2,500 | 2,499 ± 0 % (2,499 to 2,500); 0 failed; every job committed | within (the producer offers exactly 2,500) |
| Commit p99 | at most 50 ms | 36.8 ms ± 3 % (29.2 to 37.5) | within |
| Enqueue to commit p99 | at most 90 ms | 62.4 ms ± 2 % (59.1 to 63.7) | 62.4 ms; within the 90 ms line; over the superseded 60 ms line by 2.4 ms (9 of 10 runs) |
| Enqueue to commit p50 | none | 38.6 ms ± 6 % | |
| Writer busy | at most 70 % | 41.9 % ± 2 % | within |
| Jobs in flight per stream at p99 | under 256 with margin | 62.4 ms x 2,500/s | 156 of 256 |
| Jobs per batch | none | 100 (every batch full) | |
| Drain after the producer stops | about one enqueue-to-commit p99 | 262 ms ± 5 % | the last, partial batch waits for the store's 250 ms batch window; not a backlog |

The S1 shim right after, same window: 2,623 rows/s, commit p99 31.3 ms,
enqueue to commit p50 29.4 ms, p99 59.5 ms (Q7 recorded 2,626, 28.2, 30.2
and 57.8 ms). So the shim itself sits just under the 60 ms line today, and
the per-message job shape (savepoint, the machine read, the history row,
the `hub_counters` UPDATE and the machine UPDATE per event) adds about 5 ms
to commit p99 and 3 ms to enqueue to commit p99.

Reading: the writer keeps up with a wide margin (42 % busy, commit p99 37
ms); the enqueue-to-commit p99 is the batch fill (100 jobs at 2,500/s,
about 40 ms) plus the commit. It missed the 60 ms line as first written by
2.4 ms in 9 of 10 runs: a miss, not noise. The arbiter (round 4) found
that line had 1 ms of headroom over the shim it was copied from and no
product consumer tighter than the 256 in-flight cap (102 ms at this rate),
and reset it to 90 ms (fill plus the commit budget); 62.4 ms is within
it. The writer knobs were not changed: at the burst the batch size (100)
closed every batch; the 250 ms window closed only the last, partial one
(the drain). The micro-batch variant is not built and not measured: it
would cut the writer to at most 50 jobs/s at this rate, so every batch
would close on the 250 ms window and every event would wait about 250 ms
plus the commit, which raises this number instead of lowering it.

Not proven here:

- a busy host (the preliminary runs above show the tails follow load);
- Linux on btrfs;
- several streams replaying at once, and the 200 events/s per agent x N agents shape;
- the micro-batch variant;
- BatchMax 50;
- the benchmark's enqueue stamp is taken before EventIn and its result is
  read in a collector goroutine, so its boundary is slightly wider than the
  shim's `end.Sub(m.at)`; not corrected for.

Raw output (ten benchmark lines, the S1 run, the load log):
[`spikes/sqlite/results/s1-event-burst-2026-10-11.txt`](../../spikes/sqlite/results/s1-event-burst-2026-10-11.txt).
