# Approval list validator acceptance measurement

The actual `ValidateListInput` implementation at commit
`45c4ba3a07d9c711150a64b00506e7bbc765ca55` passed the page and token
parity check. In this real SQLite writer fixture, its writer callback was 35.10%
faster and allocated 49.89% fewer bytes than the old two-`List` path. The full
`Store.Write` call was 3.20% faster because the writer normally waits up to
250 ms to collect a batch. This is a local path result. A full API request and
the Q4 load budget are **NOT_CHECKED**.

## Workload and measurement boundaries

Host: Apple M4 Pro (`Mac16,11`), Darwin 27.0.0 arm64, external APFS volume,
Go 1.27.2, `CGO_ENABLED=0`. Benchstat is `golang.org/x/perf` at
`v0.0.0-20260709024250-82a0b07e230d`. Root granted a quiet-host window:
other agents held builds and tests while these measurements ran. Both variants
ran in the same process against the same real `state.db` created with
`statedb.Create/Open`. The store sets WAL, `synchronous=FULL`, macOS
`fullfsync=ON`, and a single writer (`internal/store/store.go`). The fixture has
1,002 unexpired `COMMAND_CONFIRM` approvals, each with a 2,048-byte preview.
The page-2 token comes from a real page-1 `List` of 500 rows; page 2 scans
501 rows before returning 500 and the next token.

The baseline callback runs `List` to validate input, `ExpireDue`, then `List`
for the response. The candidate callback calls the **production**
`ValidateListInput`, then the same `ExpireDue` and final `List`. `callback-ns/op`
starts when the writer callback begins and ends after the final `List`; it
measures time inside the writer transaction. `ns/op` covers the whole
`Store.Write` call, including the queue and batch wait. Each warm sample makes
three calls. No approval expires in this fixture.

The probe source is saved as `list_probe_test.go.txt`. It was supplied to
`go test` by a temporary Go overlay that replaced `list_input_test.go` only
inside the test process. No repository source file was edited. The probe's
parity test ran before timing: both callbacks returned the same 500 protobuf
messages and next token, and `List` and `ValidateListInput` each rejected the
three malformed tokens with `ErrInvalid`. The separate production test
`TestValidateListInputKeepsListErrors` covers defaults, caps, missing time,
expired and mismatched tokens, and invalid filters; that test was **not run in
this measurement window** because the overlay temporarily replaced its file.

The earlier `deff9a6827c0688d4acb9e5f6f98d49990b37058` shim probe on the
same host, OS, filesystem, and Go toolchain had a warm baseline callback median
of 28.89 ms (27.01–31.58 ms). The current paired baseline median was 25.88 ms
(18.01–30.07 ms), so the contemporaneous pair below is the comparison used
for acceptance. The predeclared threshold was at least 20% lower callback time
and 30% fewer allocated bytes with parity preserved. Both thresholds passed.

| Metric, 20 warm samples per variant | Two-`List` baseline median (range) | Actual validator median (range) | Change |
|---|---:|---:|---:|
| Writer callback | 25.88 ms (18.01–30.07) | 16.79 ms (11.33–20.77) | -35.10% |
| Full `Store.Write` | 277.84 ms (270.17–282.43) | 268.95 ms (262.55–272.49) | -3.20% |
| Allocated bytes/call | 4,315,012 (4,308,114–4,324,325) | 2,162,169 (2,157,154–2,171,272) | -49.89% |
| Allocations/call | 62,432 (62,419–62,449) | 31,251 (31,241–31,265) | -49.94% |

`benchstat.txt` reports `p=0.000, n=20` for these differences. Ten samples ran
with baseline first and ten with candidate first. Both orders showed the
candidate ahead. `cold-process.txt` records a separate first process sample:
baseline callback 31.46 ms and candidate 16.37 ms. Fixture creation warms the
database, so this is **not** an OS cold-cache measurement.

CPU profiles cover the full `Store.Write` benchmark rather than only the
callback. They show many samples in system calls and SQLite execution. Heap
profiles include process setup;
`modernc.org/sqlite.(*conn).columnText` and approval row scanning remain large
allocation sites. Benchmem, which excludes setup, is the allocation comparison.
Mutex profiles contain sparse runtime samples and do not prove any lock change.
The next measured limit is the 250 ms batch window for an isolated write, which
is an intentional store setting; this probe does not justify changing it.

The Q4/A50 load budget in `docs/specs/04-technology-review.md` requires a
500-row **store read** under 100 ms at up to 10 reads/s. This measurement uses
one isolated **writer** job with lazy expiry, so no valid within/outside-budget
claim follows. A real `ListApprovals` HTTP request under A50 read and write load,
including p99 latency, is still **NOT_CHECKED**. Cold OS pages, concurrent
clients, and broader approval distributions are also **NOT_CHECKED**.

## Reproduction and raw evidence

With `gen/go` present (`make gen` exited 0), copy
`list_probe_test.go.txt` to a temporary `.go` path. Build a Go `-overlay`
JSON file mapping the absolute canonical path of
`internal/hub/approvals/list_input_test.go` to that temporary file. This keeps
repository source untouched. Run from this worktree:

```sh
CGO_ENABLED=0 go test -overlay=OVERLAY -run '^TestListProbeParity$' -count=1 ./internal/hub/approvals
CGO_ENABLED=0 go test -overlay=OVERLAY -run '^$' -bench '^BenchmarkListProbe$' -benchtime=1x -count=1 -benchmem ./internal/hub/approvals
CGO_ENABLED=0 go test -overlay=OVERLAY -run '^$' -bench '^BenchmarkListProbe$' -benchtime=3x -count=10 -benchmem ./internal/hub/approvals
FLEET_PROBE_REVERSE=1 CGO_ENABLED=0 go test -overlay=OVERLAY -run '^$' -bench '^BenchmarkListProbe$' -benchtime=3x -count=10 -benchmem ./internal/hub/approvals
```

All four commands exited 0. Baseline and candidate profiles each used
`-benchtime=20x -count=1 -benchmem`, plus `-cpuprofile`, `-memprofile`,
`-mutexprofile`, and `-mutexprofilefraction=1`, and each exited 0. Raw benchmark
output is in `cold-process.txt`, `paired.txt`, `reverse.txt`, and the two
`*-profile.bench` files. `baseline.bench` and `candidate.bench` are the 20
warm rows each, with the variant suffix removed for `benchstat`. Profile text
is in the six `*-top.txt` files. Binary test and profile files are excluded
from this evidence folder.

This acceptance evidence can accompany the approval quota source commit. It
supports the validator before the lazy expiry write. It does not approve the
full RPC/load budget; that requires a separate A50 test.
