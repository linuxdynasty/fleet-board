# Fresh operation deadline cost probe

This probe compares the code before and after the fresh operation deadline
repair. It is evidence for the coordinator's review, not a production edit.
The baseline uses `session.go` and `session_create.go` from
`deff9a6827c0688d4acb9e5f6f98d49990b37058`; the candidate uses those
files from `4ffa40fa60dddbcb1c573d1df49e12e9862bd118`. Every other
production source file comes from the candidate. The rebased reproduction
revisions and exact object checks are in [source-identity.md](source-identity.md).
`prepare.py` now uses those reachable, identical revisions. `prepare.py` applies the same
temporary instrumentation to both arms through Go overlays; it never changes
the working source files.

The correctness test makes public TCP `CreateSession`, `SendKeys`, and approved
`DecideApproval` calls against a real `state.db`. For each call it compares the
fresh reply with a later public `GetOperation` read. Durable command deadlines
must be `fleetNow + 10 minutes` for CreateSession and `fleetNow + 2 minutes`
for the two control calls. The candidate's fresh replies must match those
stored values. The baseline is expected to return the wrong Unix-epoch
deadline: its original command has a nil deadline even though `Enqueue`
stores a filled clone. That defect is verified explicitly; it is not parity.

## Declared measurement

- **Metric boundaries:** `callback-ns/op` begins at entry to the entire
  `sendOne` or `DecideApproval` writer callback and ends when it returns, still
  inside the writer transaction. `rpc-ns/op` begins just before the public TCP
  call and ends when its response arrives. Standard `ns/op` measures that same
  timed section with Go's benchmark timer. `B/op` and `allocs/op` cover that
  timed RPC section. `GetOperation` verification and approval preparation run
  outside the timer. Profile output covers the benchmark process and is not an
  allocation comparison in place of `B/op`.
- **Workload:** one real local TCP server and durable `state.db` per benchmark
  sample, with 1,000 read and 1,000 write requests/minute permitted. The same
  machine, session, pane, principal, fixed hub time, and request sizes are used
  in both arms. Each sample sends five fresh `SendKeys` calls or decides five
  fresh `COMMAND_CONFIRM` approvals. The approvals are created through the
  public `RunHarnessCommand` path before each timed decision. Unique request
  IDs prevent replay. `GetOperation` checks every fresh operation. After the
  timer, a read transaction counts five matching `commands`/`request_log`
  pairs for `SendKeys` or five `approvals.decide_request_id`/command pairs for
  decisions. Every returned operation ID must be distinct and match its own
  request ID. The tables grow in the same order and shape in both arms; only
  the two versioned source files change behavior.
- **Samples:** one fresh-process sample per arm, then 10 warm samples per
  arm in baseline→candidate order and 10 more in candidate→baseline order.
  Each warm sample has five calls. A separate 20-call run per benchmark and arm captures
  CPU, heap, and mutex profiles. Binary profiles and test binaries stay in
  `/tmp`; this directory keeps text results only. "Cold" means a new process,
  **not** cold OS or database cache: fixture creation warms the database.
- **Host:** Mac16,11 (Apple Silicon), Darwin 27.0.0 arm64, Go 1.27.2,
  `CGO_ENABLED=0`. Source code is on the external APFS Development volume at
  `/Volumes/Development`. Both semantic runs logged their actual `state.db`
  fixture path under `/var/folders` and `df -P` mapped it to `/dev/disk3s5`,
  mounted at `/System/Volumes/Data`; `diskutil info` identifies this as the
  **internal APFS Data volume**. See `metadata-*.txt` for both fixture mounts
  and volume details. Both arms use the same compiled toolchain, SQLite pragmas,
  one writer, and host.
- **Decision threshold:** this is a correctness repair, so no speed gain is
  expected. A candidate callback median more than **1 ms** above its paired
  baseline, or a full RPC median more than **5%** above baseline, triggers
  diagnosis. These are investigation thresholds, not permission to weaken
  durability or waive the correct response. Any semantic mismatch stops the
  probe. The existing 250 ms writer batch wait may dominate full RPC time;
  neither arm changes it.

`prepare.py baseline|candidate TEMP_DIR` writes the overlays. The exact
precompiled binaries are placed outside this worktree:

```sh
python3 spikes/deadline-cost/results/prepare.py baseline /tmp/fleet-deadline-cost-baseline
python3 spikes/deadline-cost/results/prepare.py candidate /tmp/fleet-deadline-cost-candidate
CGO_ENABLED=0 go test -c -overlay=/tmp/fleet-deadline-cost-baseline/overlay.json -o /tmp/fleet-deadline-cost-baseline/api.test ./internal/hub/api
CGO_ENABLED=0 go test -c -overlay=/tmp/fleet-deadline-cost-candidate/overlay.json -o /tmp/fleet-deadline-cost-candidate/api.test ./internal/hub/api
```

The local preparation used private `GOCACHE` and `GOTMPDIR` directories. The
coordinator must explicitly release a quiet host before the following timed
command runs:

```sh
python3 spikes/deadline-cost/results/run.py --quiet-released
```

The runner records every timed command and exit code in `commands-exits.txt`,
the cold and warm raw rows, `benchstat.txt`, and text `pprof -top` reports.
Investigate a threshold crossing against those profiles before drawing an
acceptance conclusion. This probe does not measure A50 concurrent load,
offline agents, or a production network path.


## Recorded result

The timed runner and both semantic checks exited 0. There are 20 warm samples
per arm for each operation; each sample contains five fresh calls. The medians
below come from the raw rows, reproduced by `python3
spikes/deadline-cost/results/summarize.py`. See `benchstat.txt` for uncertainty.

| Measure | Baseline | Candidate | Change |
|---|---:|---:|---:|
| SendKeys writer callback | 1.413 ms | 1.617 ms | +0.204 ms |
| SendKeys full RPC | 260.125 ms | 260.526 ms | +0.15% |
| SendKeys allocations/call | 559 | 621.5 | +11.18% |
| DecideApproval writer callback | 2.510 ms | 2.549 ms | +0.039 ms |
| DecideApproval full RPC | 261.441 ms | 261.247 ms | -0.07% |
| DecideApproval allocations/call | 1,221 | 1,286 | +5.32% |

Neither declared investigation threshold was crossed. The added indexed read
has a measurable allocation cost. These samples do not establish zero latency
cost: callback medians increased, with substantial sample variation. The
callback differences were not statistically significant in benchstat (SendKeys
p=0.072; DecideApproval p=0.862). Allocation counts increased in both cases
(p=0.000). Bytes/call varied too much to claim a memory reduction.

CPU profiles mostly sample runtime waits and system calls; the candidate CPU
profile contains only 610 ms of samples over 20.32 seconds. Heap profiles include
fixture and server setup, including compression writers and Connect handlers.
They do not isolate the extra operation lookup or explain all allocation changes.
The raw timed per-call counters support the comparison; the coarse profiles do
not establish a precise cause for every difference.

This closes the isolated cost question for this correctness repair. A50 load,
full concurrent traffic, Linux, cold OS cache, offline behavior and a production
network are NOT_CHECKED. The wrong baseline fresh deadlines and the correct
candidate fresh/stored/public-read values were checked explicitly in both arms.
