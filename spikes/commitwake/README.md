# Commit wake measurements

On 2026-10-18, the unused `Store.CommitWake` foundation showed no measurable
regression against its baseline. This qualifies the absent-reader case only.
The full channel never blocks the writer. It does not qualify an active Watch
dispatcher, browser fan-out, or the complete A50 workload.

## Reproduction

Baseline `ef3ead1925732ba3993371059443a8a1ee380c27`; candidate
`ebc58249174acf310572eaabd00294743b259a6f` was the measured local revision.
After rebasing, use reachable ancestor
`966a5b4cce07aaf6b2fc50015afc649ec5dbabdb` to reproduce its store and ingest
benchmarks. Their source and module inputs are byte-identical; this is source
identity proof, not a second measurement. See [source-identity.md](source-identity.md).
Same Apple M4 Pro (12 cores),
64 GB RAM, Darwin kernel 27.0.0, Go 1.27.2, internal SSD/APFS. Each invocation creates
a fresh temporary SQLite database using the benchmark's normal durable store.
Other builds and tests were paused for the entire measurement window.

Build the two packages with `CGO_ENABLED=0 go test -c`, once per revision.
Run each binary in its package directory. Alternate baseline/candidate order
on successive rounds, ten invocations per side:

```sh
store.test -test.run='^$' -test.bench='^BenchmarkWriterBatch$'   -test.benchmem -test.count=1 -test.benchtime=1s
FLEET_EVENT_BURST=1 ingest.test -test.run='^$'   -test.bench='^BenchmarkEventBurst$' -test.benchmem   -test.count=1 -test.benchtime=1x
```

Compare each pair of raw text files with pinned benchstat. All forty measured
invocations exited 0. An initial temporary runner used a wrong binary name and
exited 1 before running any burst sample; the corrected twenty-sample series
is the one reported here. Generation, four binary builds and both comparisons
exited 0. No durability setting changed.

## Results

Values below are medians across ten runs. Raw benchmark output and complete
benchstat tables are in [results/](results/).

| Measure | Baseline | Candidate |
| --- | ---: | ---: |
| Writer batch, 1 job | 3.928 ms | 3.904 ms |
| Writer batch, 10 jobs | 4.109 ms | 4.166 ms |
| Writer batch, 100 jobs | 5.229 ms | 5.142 ms |
| Burst committed jobs/s | 2,499 | 2,499 |
| Burst commit p99 | 24.85 ms | 24.86 ms |
| Burst submit-to-result p99 | 56.50 ms | 56.44 ms |
| Burst writer busy | 31.59% | 31.77% |
| Burst final drain | 254.1 ms | 250.8 ms |
| Failed jobs | 0 | 0 |

All offered jobs committed: the generator offers 2,500/s for sixty seconds;
reported throughput divides all committed jobs by the offered window.
Final drain is reported separately. Every burst sample stayed within
[04c's budgets](../../docs/specs/04c-event-ingest-benchmark.md): commit p99
50 ms, submit-to-result p99 90 ms, writer busy 70%. The submit-to-result p99 implies about 141 jobs in flight
at the offered rate, below the 256 cap; this is an estimate, not an observed
maximum queue depth.
The last partial batch normally waits for the 250 ms flush timer.
No comparison showed a statistically significant difference.

Baseline submit-to-result p99 ranged 56.06–56.77 ms; candidate 56.13–57.09 ms.
The first invocation per side gave 56.14/56.80 ms; later-run medians were
56.51/56.43 ms. This separates first process invocation from warmed host runs;
it is **not** a cold OS page-cache test.

## Boundaries and limits

WriterBatch time covers submission through durable outcomes. Burst commit and
busy metrics stop immediately after the database transaction, before the wake
send. Submit-to-result timing includes that send and result collection, so the
commit metric alone cannot show wake overhead. The candidate has no wake reader;
after the first send, the buffer stays full and sends take the default branch.
This checks backpressure, not the highest possible CPU cost of an active reader.

Separate ten-second burst runs collected CPU, heap and mutex profiles on each
revision, with `FLEET_EVENT_BURST_DURATION=10s`, `-test.cpuprofile`,
`-test.memprofile`, `-test.mutexprofile` and `-test.mutexprofilefraction=1`.
Both exited 0 and were excluded from the sixty-second result medians.
Profiles remain local; text summaries accompany the raw results.

NOT_CHECKED: Linux, cold OS page cache, concurrent browser reads, multiple
streams, active Watch dispatch, and fan-out. Repeat the paired burst with the
actual dispatcher before claiming its load acceptance. Preserve delivery order,
zero failed jobs and the documented budgets in that follow-up.
