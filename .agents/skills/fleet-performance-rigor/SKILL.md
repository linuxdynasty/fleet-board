---
name: fleet-performance-rigor
description: "Use when proving or improving a fleet-board latency, throughput, disk, memory or load claim: store write rates, fan-out, log volume, parser cost, dispatch latency. Measure before and after against the documented load budgets; find an unknown cause first with fleet-diagnostic-rigor."
---

# Performance rigor

Correctness and durability come first, speed second. A faster wrong answer, a
weakened `synchronous=FULL`, a dropped never-dropped event or a serialized
workaround is a failure. Use `fleet-diagnostic-rigor` first when the bottleneck
is unknown.

## Know the budget

The load model, its budgets and the measured answers are in
[04-technology-review.md](../../../docs/specs/04-technology-review.md#load-model-a4-and-a50);
raw output is under [`spikes/`](../../../spikes/README.md). Compare store write
rate and commit latency, sidebar read latency, log chunk rate and size, and
browser fan-out against the numbers there; do not copy them into a claim or a
doc.

A claim is "better than budget", "within budget" or "outside budget"; say which.

## Prove the theory first

Before building on a performance theory (a cache, an index, a batch size, a
pragma, a queue), prove it with the cheapest shim: a throwaway Go benchmark, a
`spikes/`-style program, `EXPLAIN QUERY PLAN`, or a microbenchmark on
representative data. fleet-board already did this for step 4; a new theory gets
the same. Do not land production code on an unmeasured one.

## Measure

1. Declare before implementing: the metric and its exact start and end events,
   the workload (rates, sizes, fleet shape), the baseline and its normal band,
   the minimum worthwhile gain, the stop threshold, and the correctness
   invariant that must still hold.
2. Same machine, same data, same pragmas, same Go version, quiet host. Record
   the machine, OS, filesystem (APFS vs btrfs matters for fsync) and commit.
   Never compare totals with different boundaries; label them non-comparable.
3. Use `go test -bench` with `-count` of at least 10 and `benchstat`
   (`~/go/bin/benchstat`) for before/after; use `-benchmem`. Use pprof (CPU,
   heap, mutex) before and after, not instead of the benchmark.
4. Synthetic data must not flatter the result: random bytes are pessimistic for
   compression and page reuse, real captures are not. Say which was used.
5. Take a cold run and warm runs separately and report both.
6. Do not run measurements while another agent builds or tests on the host;
   a wall-time claim owns the machine.

## Never trade away

- Durability (`synchronous=FULL`, `fullfsync`, ack after commit).
- Never-dropped event delivery and `UNKNOWN_OUTCOME` handling.
- One writer per file. A faster approach with two writers needs an ADR.
- Parsers never guess to be faster; an unknown reading stays unknown.

## Report

Baseline and candidate numbers with units, the command used, the iteration
count and spread, the machine, what the change does *not* prove (for example
a cold-cache read of `logs.db` at 50 GiB is unproven), and the next measured
bottleneck. A change
that is only a local optimization says so rather than claiming an end-to-end
gain. Hand off a proved change as an implementation task with its acceptance
benchmark; this role does not edit code.
