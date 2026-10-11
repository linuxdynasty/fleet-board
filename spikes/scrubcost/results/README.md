# Hub scrubber cost (plan row 5l, #33)

Raw `go test -bench` output behind
[04b](../../../docs/specs/04b-hub-scrubber-cost.md). The benchmarks are in
`internal/scrub/hub_pass_test.go`, not here; this folder only keeps the output.
Machine: Apple M4 Pro, 12 cores, 64 GB, macOS 27.0.1, Go 1.27.2, 2026-10-10.

| File | Command | Load average |
|---|---|---|
| `hub-pass-run1.txt` | `go test -run '^$' -bench 'BenchmarkHubPass' -count=10 ./internal/scrub/` | 3.5 to 4 |
| `hub-load-run1.txt` | `go test -run '^$' -bench 'BenchmarkHubLoad' -count=10 ./internal/scrub/` (before the usage and StartGoal scenarios) | 3.5 to 5 |
| `hub-pass-run2-benchmem.txt` | `... -bench 'BenchmarkHubPass$' -benchmem -count=10 ...` | 10 to 13 |
| `hub-load-run2-benchmem.txt` | `... -bench BenchmarkHubLoad -benchmem -count=10 ...` | 5 to 10 |
| `corpus-row6.jsonl` | the executor's own result messages, one JSON string per line (plan row 6 PR 6e, ruling fork 2): `FLEET_EXEC_CORPUS_OUT=<file> go test -count=1 ./internal/agent/exec/` | |
| `corpus-row6-bench.txt`, `corpus-row6-benchstat.txt` | `FLEET_SCRUB_BENCH_CORPUS=spikes/scrubcost/results/corpus-row6.jsonl go test -run '^$' -bench BenchmarkHubPassFile -count=10 ./internal/scrub/`, then `benchstat` | about 9.3 to 9.8 (other agents running) |
| `text-run2-benchmem.txt` | `... -bench 'BenchmarkTextHostile$\|BenchmarkTextMessage$\|BenchmarkTextScrubbed$' -benchmem -count=10 ...` | 5 to 6.6 |

Read with `benchstat <file>`. Run 1 is the cleaner timing; run 2 was taken while
other agents were running tests, so its times spread more (up to ±100 % on one
256 B row); use run 2 for allocations and for the scenarios run 1 lacks.

About the row 6 corpus: these are the executor's refusal messages only; the first command handler (row 8) appends its real result messages and reruns.
