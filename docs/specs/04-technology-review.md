# 04. Technology review

- Status: Accepted
- Date: 2026-10-02
- Inputs: the [load model](#load-model-a4-and-a50) below (questions Q1 to Q9),
  [03f](03f-hub-internals.md) (concurrency model, data model),
  ADRs [0001](../adr/0001-system-architecture.md),
  [0002](../adr/0002-storage-sqlite.md), [0003](../adr/0003-transport-grpc.md),
  [0007](../adr/0007-web-ui-react.md), [0010](../adr/0010-session-logs.md),
  [0013](../adr/0013-live-state-retention.md)
- Evidence: spikes S1 and S2 in [`spikes/`](../../spikes/README.md), with raw
  output in `spikes/*/results/`
- Output: ADRs [0014](../adr/0014-storage-confirmed.md),
  [0015](../adr/0015-session-logs-in-sqlite.md),
  [0016](../adr/0016-browser-transport.md); depends on the hub API and
  log-framing fields in [0017](../adr/0017-contract-packages.md) and
  [0018](../adr/0018-hub-api-and-contract-amendments.md) (merged first)

Step 3 produced numbers; this step checks each technology choice against them,
proves the risky ones with a spike before deciding, and cites the vendor
documentation for every locking, durability and consistency claim. Vendor
pages were read on 2026-10-02.

## Summary

| Question | Decision | Proven by |
|---|---|---|
| Q1 durable store | **SQLite stays.** `modernc.org/sqlite`, WAL, `synchronous=FULL`, `fullfsync=ON`, one writer, 250 ms / 100-message batches | S1: 2,543 rows/s for 60 s, commit p99 ≤ 28 ms, readers never blocked |
| Q2 pane summaries | Every update persisted, latest per pane (ADR 0013); the final summary of each pane moves to `logs.db` when its session ends | S1: 100 updates/s costs 0.79 MB/s of disk writes at A50 |
| Q3 queue or broker | **None.** In-process bounded channels as in 03f | Load is 110 msg/s; at-least-once already comes from the agent ring and the outbox |
| Q4 hot state | **Read from the store.** No cache process, no in-memory copy of record | S1: 500-row sidebar read p99 21 ms under burst |
| Q5 browser fan-out | **Connect** (unary + server streaming, one stream per tab); terminal attach over **WebSocket** with byte acks | Vendor docs (browsers cannot stream requests) |
| Q6 log storage | **`logs.db`**: zstd chunks as rows in a second SQLite file with its own writer; replaces "files on disk" in ADR 0010 | S1: 30 chunks/s commit p99 ≤ 19 ms; delete one session 6 ms; prune and vacuum in ≈ 55 ms steps; at the 50 GiB cap commit p99 ≤ 24 ms, vacuum step p99 ≤ 74 ms (2026-10-08) |
| Q6 chunk framing | One zstd frame per stream for ≤ 5 min or 4 MiB raw, flushed into a chunk every 10 s; replay seeks by frame | S2: 14× vs 8× for independent 10 s frames (Codex, 60 min); Claude Code 2026-10-12 confirms the same 14× vs 8× |
| Q6 log volume | The first estimate was low: Codex output compresses 15×, not 115× (Muse); A50 is **50–270 GB/month** depending on the harness mix; default size cap 50 GiB | S2 |
| Q7 replay budget | 200 ev/s per agent, 2,500 ev/s per hub, as proposed | S1 burst |
| Q8 provider limits | Polling only in v1; webhook ingress later and optional | GitHub docs: 5,000 req/h per installation; 304s free |
| Q9 agent persistence | `agent.db` (same driver) for command log, event ring and spool chunks; raw `pipe-pane` output in plain append files | Same engine as S1; agent rates ≤ 1 tx/s |
| UI build and serve | Vite → `web/dist` → `go:embed` → `http.FileServerFS`; Node only at build time | Go docs |
| Redis, Postgres, NATS | **Not used** | See [Rejected](#rejected-options) |

## Load model (A4 and A50)

**Fleet assumptions.** *A4* (the author's fleet, design.md): 4 machines, 3
accounts, 1 repo on 1 integration, 20 observed panes, 10 goal panes capturing
logs (the sum of `max_active` is 2+2+4+2 = 10), 2 browsers. *A50*: 50
machines × 10 panes = 500 observed panes, 3 accounts (accounts scale
with people, not machines), 5 repos, 300 goal panes capturing (`max_total` ≈ 6
per machine), 150 active goals (`max_active` ≈ 3), 10 browsers.

**Measured inputs ([verified-facts](../research/verified-facts.md#measured-one-real-goal), 2026-10-01, Muse on a real goal).** Raw terminal
output 26.9 MB/h per busy pane = 26.9 × 10⁶ ÷ 3600 = **7.47 KB/s**; redraw
floor 274 KB/min = **4.57 KB/s** even when quiet; zstd-3 0.23 MB/h = 234 KB ÷
3600 = **65 B/s** per pane; Muse transcript 13 MB/h = **3.6 KB/s** raw.
**Contract rates (02).** Heartbeat every 15 s; summaries ≤ 1 per pane per
5 s; resource sample every 30 s; probes every 10 min per account.
**Modelling assumptions** (stated so a later measurement can change them): heartbeat 0.5
KB, summary 2 KB, activity transition 0.3 KB at 2 per minute per active goal
pane, goal state and branch state 2 KB at 6 per hour per goal each, prompt or
condition 2 per hour per active goal, usage reading 3 KB, command or result 1
KB, snapshot 100 KB, log chunk every 10 s per stream.

| # | Workflow / stream | A4 steady | A50 steady | Peak | Durability | Ordering | Browser fan-out |
|---|---|---|---|---|---|---|---|
| 1 | W1/W2 snapshots | ≈ 0 (reconnects rare) | ≈ 0 | hub restart: 4 × 100 KB in ≤ 60 s = 6.7 KB/s; 50 × 100 KB = 5 MB in 60 s = **83 KB/s**, 50 apply-txs | must-persist | Hello → Snapshot → results → events, per agent | none (UI re-reads) |
| 2 | W2 ring replay | ≈ 0 | ≈ 0 | per agent ≤ 200 ev/s × 0.5 KB = 100 KB/s; 50 agents uncapped = 10,000 ev/s = 5 MB/s → **hub-wide budget 2,500 ev/s (1.25 MB/s) for ~1 min** | must-persist (history) | per agent `seq` | none |
| 3 | A5 heartbeats | 4 ÷ 15 = 0.27/s × 0.5 KB = 0.13 KB/s | 50 ÷ 15 = 3.3/s = **1.7 KB/s** | same | can-lose (latest in memory; `last_seen` written at most once a minute per machine: 0.07 rows/s at A4, 0.83 at A50) | none | coalesced to the summary strip, 1/s per browser |
| 4 | B2 resource samples | 4 ÷ 30 = 0.13/s = 0.07 KB/s | 50 ÷ 30 = 1.7/s = **0.8 KB/s** | same | can-lose; only the latest sample is kept, in memory (the contract has no 24 h chart), so no durable rows | none | Machines view only |
| 5 | E5 pane summaries | 20 × 0.2 = 4/s × 2 KB = **8 KB/s** | 500 × 0.2 = 100/s × 2 KB = **200 KB/s** (720 MB/h) | same | can-lose; **latest per pane only** (persisting 1 week = 100 × 86,400 = 8.6 M rows/day, 17 GB/day; see Q2) | per pane, latest wins | unfiltered: 2 browsers × 8 = 16 KB/s; 10 × 200 = **2 MB/s**; filtered to ~30 visible panes per browser: 10 × 6/s × 2 KB = 120 KB/s |
| 6 | E4 activity transitions | 10 × 2 ÷ 60 = 0.33/s × 0.3 KB = 0.1 KB/s | 150 × 2 ÷ 60 = **5/s**, 1.5 KB/s | provider incident: 150 in 10 s = 15/s; replay bursts (row 2) | must-persist, never dropped | per agent `seq` | Session view, per visible pane |
| 7 | G2/H3 goal + branch state | 10 × 12 ÷ 3600 = 0.03/s, 0.07 KB/s | 300 × 12 ÷ 3600 = **1/s**, 2 KB/s | dispatch wave: 50 goals × 2 in 1 min = 1.7/s | must-persist, never dropped | per agent `seq` | all browsers: 10 × 1/s = 10 msg/s, 20 KB/s |
| 8 | E6/E12 prompts + conditions | 10 × 2 ÷ 3600 = 0.006/s | 150 × 2 ÷ 3600 = **0.08/s** | provider incident: 150 conditions in 10 s = 15/s, then 150 `cleared` | must-persist, never dropped | per agent `seq` | all browsers (Triage) |
| 9 | W3 usage readings + probe health | 3 × 6 ÷ 3600 = 0.005/s × 3 KB = 15 B/s; event probes ≤ 0.05/s; passive ≤ 4 ÷ 60 = 0.07/s | probes same 0.005/s (accounts do not scale); passive ≤ 50 ÷ 60 = **0.8/s**, 0.8 KB/s | goal start/finish wave: ≤ 1 per account per minute | must-persist (latest + history) | by `read_time` per account | all browsers (strip), coalesced |
| 10 | Commands, acks, progress, results | ≈ 0.05/s, 50 B/s | ≈ 0.5/s (dispatch ≤ 2/min + prompt answers 0.08/s + nudges + probes), **0.5 KB/s** | reconnect re-issue: pending outbox per machine (tens) | must-persist (outbox, results) | per epoch `seq`; results idempotent by id | Triage/Session (status) |
| 11 | W4 sync, GitHub polling | issues 60/h + claims 10 items × 12/h = 120/h + CR batch 60/h + full poll 2/h ≈ **250 req/h = 0.07 req/s**; ≈ 30 % non-304 × 20 KB = 1.5 MB/h | issues 5 × 60 = 300/h + claims 50 × 12 = 600/h + CR 300 CRs ÷ 50 per batch × 60 = 360/h + full 10/h ≈ **1,300 req/h = 0.36 req/s**, ≈ 8 MB/h | verify-now bursts ≤ 6/min | mirrors re-fetchable; cursors must-persist | per resource `update_time` | Work view on change |
| 11b | W4 with webhooks (GitLab, Jira Cloud) | inbound ≈ 60 events/h | inbound ≈ **300 events/h = 0.08/s**; backstop poll every 10 min: 5 repos × 3 resources × 6/h = 90 req/h | provider burst (bulk label change): 100 events/s for seconds | inbound events can-lose (backstop poll repairs) | by `update_time` | same |
| 12 | W9 terminal logs, shipped | 10 × 65 B/s = **0.65 KB/s** = 2.3 MB/h = 56 MB/day = 1.7 GB/month; 1 chunk/s | 300 × 65 = **19.5 KB/s** = 70 MB/h = 1.7 GB/day = **50 GB/month**; 30 chunks/s (30 index rows/s batched); all 500 captured: 32.5 KB/s = 117 MB/h = 84 GB/month | reconnect after 8 h offline: spool per machine ≤ 6 panes × 65 B/s × 28,800 s = 11 MB, shipped at line rate | must-persist (file + index), tolerates re-send | per stream byte offset | live tail only to viewers of that session; replay on demand |
| 12a | W9 raw into agent spools (not shipped) | 10 × 7.47 = 75 KB/s across agents | 300 × 7.47 = **2.24 MB/s** across agents (≤ 75 KB/s per machine); compressed in spool: 65 B/s per pane | — | agent-local | — | — |
| 13 | K3 transcripts (v3, opt-in) | 10 × 3.6 = 36 KB/s raw | 300 × 3.6 = **1.08 MB/s raw**; compression unmeasured | — | must-persist if enabled | by offset | replay only |
| 14 | J1 attach | 2 attaches × ≤ 7.5 KB/s = 15 KB/s | 10 × 7.5 = **75 KB/s** | redraw on resize | never dropped (PTY blocks) | per attach | 1:1 |
| 15 | W10/W11 settings, drift | rare | rare (≤ 100 catalog rows per change) | fleet-wide setting change: 50 Settings + 50 acks | must-persist | by generation | Machines and Settings views |

**Totals.**

| | A4 | A50 |
|---|---|---|
| Inbound to hub, steady | ≈ **9 KB/s** (8 summaries + 0.65 logs + 0.35 everything else) | ≈ **227 KB/s** (200 summaries + 19.5 logs + 7.5 everything else) ≈ 0.8 GB/h, of which 90 % is can-lose summaries |
| Durable bytes, steady | ≈ **0.8 KB/s** | ≈ **25 KB/s** (19.5 logs to `logs.db` + ~5 events/state to the store) |
| Durable rows, steady | ≈ **2.1 rows/s** (log index 1, activity 0.33, heartbeat `last_seen` 0.07, sync ≤ 0.5, rest ≈ 0.2) | ≈ **48 rows/s** (log index 30, sync ≤ 10, activity 5, heartbeat `last_seen` 0.83, goal/branch 1, usage 0.8, commands 0.5) |
| Durable rows, peak | hub restart: 4 snapshots + replay ≤ 800 ev/s for seconds | hub restart: **2,500 ev/s for ~60 s** (budgeted), 50 snapshot txs; provider incident: 30 never-dropped events/s for 10 s |
| Store growth (retained) | events 0.4/s × 0.5 KB × 604,800 = **120 MB rolling week**; logs 1.7 GB/month | events 7/s × 0.5 KB × 604,800 = **2.1 GB rolling week**; logs **50–84 GB/month** until the size cap |
| Browser fan-out | ≤ 20 KB/s | 2 MB/s unfiltered summaries; **≈ 140 KB/s** with per-pane subscriptions (120 summaries + 20 state) |
| Provider requests | 250 req/h | 1,300 req/h polling; ≈ 90 req/h + 300 events/h with webhooks |

## Q1. Durable store

**Requirement (load model).** One writer, the A50 steady rate in the load model's
"Durable rows, steady" row (≈ 48 rows/s, the sum of its parts) and ≈ 2,500 rows/s for
60 s at A50 (the older ≈ 52 in [ADR 0014](../adr/0014-storage-confirmed.md) has no
breakdown, so 48 is the number to use); ≤ 10 browsers reading without blocking the
writer; zero-dependency install; a laptop hub that can lose power.

**Vendor facts.**

- WAL lets readers and a writer run at the same time; there is one writer at a
  time; a long-running reader can keep a checkpoint from finishing and the WAL
  then grows; WAL needs all processes on one host (no network filesystem);
  auto-checkpoint runs at 1,000 pages by default
  ([sqlite.org/wal.html](https://www.sqlite.org/wal.html)).
- With `synchronous=NORMAL` in WAL mode a committed transaction "might roll
  back following a power loss or system crash"; every mode is durable across
  an application crash. `fullfsync` (macOS only) makes syncs use
  `F_FULLFSYNC`; default off
  ([sqlite.org/pragma.html](https://www.sqlite.org/pragma.html)).
- On macOS `fsync()` moves data to the drive, which may keep it in its cache;
  only `F_FULLFSYNC` asks the drive to write it to permanent storage
  ([Apple fsync(2)](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html)).
  Go's `os.File.Sync` has used `F_FULLFSYNC` on darwin since Go 1.12
  ([go1.12 notes](https://go.dev/doc/go1.12),
  [fd_fsync_darwin.go](https://github.com/golang/go/blob/master/src/internal/poll/fd_fsync_darwin.go)).
- `BEGIN DEFERRED` can fail with `SQLITE_BUSY` when it upgrades to a write;
  `BEGIN IMMEDIATE` takes the write lock at the start
  ([lang_transaction.html](https://www.sqlite.org/lang_transaction.html)).
- `VACUUM INTO` writes a consistent snapshot of a live database
  ([lang_vacuum.html](https://www.sqlite.org/lang_vacuum.html)).
- Pure-Go drivers: `modernc.org/sqlite` v1.60.1 (C SQLite translated to Go,
  23 platforms including darwin/linux amd64/arm64, 1.3–2× slower than C on CPU
  work; [pkg.go.dev](https://pkg.go.dev/modernc.org/sqlite)) and
  `ncruces/go-sqlite3` v0.35.6 (Wasm build translated to Go, OFD locks and
  mmap WAL index on macOS and Linux;
  [vfs README](https://github.com/ncruces/go-sqlite3/blob/main/vfs/README.md)).
  Both embed SQLite 3.53.4.

**S1 results (hub MacBook, Apple SSD; Linux below).**

Durable commit, 300 one-row transactions:

| Driver | `synchronous` | `fullfsync` | p50 | p99 |
|---|---|---|---|---|
| modernc | FULL | off | 47 µs | 1.3 ms |
| modernc | FULL | **on** | **4.0 ms** | 6.0 ms |
| ncruces | FULL | off | 59 µs | 117 µs |
| ncruces | FULL | **on** | **4.1 ms** | 7.1 ms |
| either | NORMAL | off | 22 µs | 50 µs |

A bare `F_FULLFSYNC` on the same disk is 3.9 ms p50, so both drivers pass
`fullfsync` through to the drive; without it a "durable" commit costs 50 µs
and is not durable on this hardware.

A50 load, 60 s, `fullfsync=ON`, 50 ms batch window (03f as written):

| Driver | Scenario | Rows/s | Commits/s | Commit p99 | Enqueue → commit p99 | Sidebar (500 rows) p99 | Disk written |
|---|---|---|---|---|---|---|---|
| modernc | steady | 127 | 17.6 | 11.8 ms | 65 ms | 21 ms | 1.19 MB/s |
| modernc | burst | 2,626 | 26.3 | 28.2 ms | 58 ms | 21 ms | 15.1 MB/s |
| ncruces | steady | 127 | 17.6 | 12.0 ms (max 197 ms) | 67 ms | 14 ms | 1.19 MB/s |
| ncruces | burst | 2,626 | 26.3 | 27.3 ms | 58 ms | 15 ms | 15.2 MB/s |

What drives disk writes (modernc, steady, 40 s each):

| Summaries/s | Summary size | Batch window | Commits/s | Disk written |
|---|---|---|---|---|
| 100 | 2 KB | 50 ms | 17.5 | 1,187 KB/s |
| 100 | 600 B | 50 ms | 17.5 | 1,165 KB/s |
| 0 | — | 50 ms | 17.7 | 493 KB/s |
| 100 | 2 KB | **250 ms** | 3.9 | **793 KB/s** |
| 100 | 2 KB | 1 s | 1.3 | 676 KB/s |

Linux (linux-box, x86_64, btrfs, load average 15 from a running Codex goal,
normal I/O priority; `fullfsync` is a no-op there):

| Scenario | Commit p50 / p99 | Enqueue → commit p99 | Sidebar p99 | Disk written |
|---|---|---|---|---|
| durable 1-row commit | 1.0 ms / 2.6 ms | — | — | — |
| steady, 50 ms window | 1.8 ms / 9.2 ms | 58 ms | 37 ms | 2.2 MB/s |
| burst, 50 ms window | 3.9 ms / 21.6 ms | 54 ms | 37 ms | 13.7 MB/s |
| steady, 250 ms window | 2.2 ms / 15.9 ms | 255 ms | 32 ms | 0.98 MB/s |

A first Linux run under the idle I/O class (`ionice -c3`, chosen to stay out
of the running goal's way) showed 350–950 ms commit tails; the normal-priority
re-run above shows they came from the I/O class, not from SQLite or btrfs.
btrfs (copy-on-write) writes about twice the bytes of APFS at the 50 ms
window; the 250 ms window halves it.

Every commit rewrites whole 4 KiB pages twice (WAL, then checkpoint), so
volume follows the commit count and the number of distinct pages touched, not
row size; compressing summaries saves nothing.

**Decision.**

- **Engine and driver:** SQLite via `modernc.org/sqlite` (no cgo). Both
  drivers met every number; modernc is the more widely used and had no
  outlier commit in S1. `ncruces/go-sqlite3` is the fallback behind the same
  `store` interface.
- **Pragmas:** `journal_mode=WAL`, `synchronous=FULL`, `fullfsync=ON` (no-op
  off macOS), `busy_timeout=5000`, `foreign_keys=ON`,
  `journal_size_limit=64 MiB`.
- **Writer:** one connection, `BEGIN IMMEDIATE`, batches of ≤ 100 messages or
  **250 ms**, whichever comes first (03f said 50 ms). This cuts disk writes by a
  third at a cost of ≈ 270 ms p99 from event to commit; no workflow has a
  tighter deadline (the fastest is the 2 min `SendKeys` window). The window is
  a setting.
- **Readers:** pool of ≤ 10 `query_only` connections; read transactions stay
  short so checkpoints are never starved; no read transaction spans a network
  call or a stream to a browser.
- **Backup:** nightly `VACUUM INTO` of `state.db` and on `fleetd backup` (a local command: a backup holds the CA key).
- **Sustained capacity:** at the decided settings the A50 steady write is
  ≈ 4 commits/s × ≈ 7 ms = 3 % of the writer's time, and the restart burst
  ≈ 25 commits/s × ≤ 28 ms p99 ≤ 70 %; rows themselves are cheap (pure-Go
  drivers insert ≈ 370–410 k rows/s inside one transaction,
  [go-sqlite-bench](https://github.com/cvilsmeier/go-sqlite-bench)).

## Q2. Pane summaries

Owner decision (2026-10-02): every summary update is written (ADR 0013's
latest-per-pane row, overwritten in place), and when a session ends the last
summary of each of its panes moves to `logs.db` next to that session's
terminal log and is pruned with it.

Cost from S1 at A50: ≈ 0.8 MB/s (APFS) to 1.0 MB/s (btrfs) of disk writes at
the 250 ms window, ≈ 68–85 GB a day of SSD writes on the hub; at A4 (≈ 4
updates/s) it is negligible. The agent already sends a summary only when the pane changed and at most every 5 s
(02), so the rate is an upper bound. If SSD wear becomes a concern, writing
the latest summary per pane every 30 s cuts total disk writes to ≈ 0.3 MB/s
(S1, `summary-rate=17`, 250 ms window); that is a setting, not a design change.

## Q3. Queue or broker

The hub-internal flow is ≈ 110 messages/s steady and bursts of up to
10,000/s that the per-agent replay cap (200 ev/s) and the hub budget
(2,500 ev/s) bound before they reach the writer. Every never-dropped kind is
already at-least-once from the agent's persisted ring, and every command is a
row in the outbox, so no hub-side queue needs its own durability.

- HTTP/2 flow control is per stream and per connection
  ([RFC 9113 §5.2](https://www.rfc-editor.org/rfc/rfc9113.html#section-5.2)),
  so one agent's full window blocks only that agent's stream.
- grpc-go sizes windows dynamically from a bandwidth-delay estimate
  ([grpc.io blog](https://grpc.io/blog/grpc-go-perf-improvements/)); setting
  `InitialWindowSize`/`InitialConnWindowSize` turns that off (grpc-go v1.84
  source), so fleetd leaves them unset.
- Keepalive pings detect dead connections; server-side enforcement must allow
  the agent's ping interval or the server closes with `too_many_pings`
  ([grpc.io keepalive](https://grpc.io/docs/guides/keepalive/)).

**Decision:** in-process bounded channels exactly as 03f draws them: per-agent
`connReader` queue (sheds droppable kinds only), a bounded ingest channel, a
token bucket per agent and one for the hub for replayed events.

## Q4. Hot state

The load model needs ≤ 10 reads/s of ≈ 500 rows under 100 ms. S1 measured the
500-row sidebar at 14–21 ms p99 and 30 visible summaries at ≤ 2.5 ms p99,
including during the 2,500 rows/s burst. **Decision:** the UI and the
scheduler read the store; there is no cache process and no in-memory copy of
record. The fan-out bus still pushes changes after commit so browsers do not
poll.

## Q5. Browser fan-out and terminal attach

- Connect for Go v1.21.0 serves the Connect, gRPC and gRPC-Web protocols from
  one handler ([connectrpc.com/docs/go](https://connectrpc.com/docs/go/getting-started/)).
- Browsers support only server streaming, because they do not stream request
  bodies ([connect-web](https://connectrpc.com/docs/web/getting-started/));
  only bidirectional streaming needs HTTP/2
  ([protocol](https://connectrpc.com/docs/protocol/)).
- Without HTTP/2 a browser allows 6 event-stream connections per origin,
  shared by all tabs ([MDN EventSource](https://developer.mozilla.org/en-US/docs/Web/API/EventSource)).
  `local_trusted` mode on plain-HTTP localhost hits this limit.
- Browser WebSockets have no backpressure; fast input is buffered in memory
  ([MDN WebSockets](https://developer.mozilla.org/en-US/docs/Web/API/WebSockets_API));
  the backpressured `WebSocketStream` is experimental.
- xterm.js discards data past a 50 MB write buffer and recommends acking
  writes with callbacks and pausing the source between watermarks
  ([xterm.js flow control](https://xtermjs.org/docs/guides/flowcontrol/)).

**Decision.**

- **API:** every UI, CLI and (later) phone call is a `fleet.hub.v1` Connect
  handler ([ADR 0017](../adr/0017-contract-packages.md)); the TS client
  is generated (`@connectrpc/connect-web` 2.2.0). This settles ADR 0003's open
  library question.
- **Live updates:** one server stream per browser tab or phone
  (`FleetService.Watch`, [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md))
  carries every topic the client asked for, each with its own filter. The
  subscription is in the request, never server-side state keyed by a stream
  id: to change it the client reopens the stream with its last `resume_token`
  and the new list; unchanged subscriptions resume and new or changed ones
  start with their initial state (02c), which survives phone reconnects.
  When the hub cannot honour the token (older than the change log, hub
  restarted) it sends `RESYNC_REQUIRED` and the client re-reads. One stream per
  tab stays under the HTTP/1.1 limit. Slow consumers lose summaries only
  (latest wins per pane).
- **Terminal attach:** WebSocket carrying the hub API's
  `AttachTerminalRequest`/`AttachTerminalResponse` messages (the CLI uses
  HTTP/2 streaming). The browser acks bytes with `AttachAck` after xterm.js
  has written them; the hub stops reading
  that attach's `Terminal` gRPC stream when unacked bytes exceed a high-water
  mark (proposal 256 KiB), so HTTP/2 flow control backs up to the agent and
  its PTY read blocks. Output is never dropped end to end.

## Q6. Session log storage

Options considered:

| Option | Syncs per batch at A50 | Delete one session | Crash between data and index |
|---|---|---|---|
| A. File per `(session, stream, day)` + index rows in `state.db` (ADR 0010) | up to ≈ 30 files × 3.9 ms + index commit | delete files + rows | possible; needs startup truncation of a partial frame |
| **B. Chunks as rows in `logs.db`, own writer** | **1** | one `DELETE` | impossible; chunk and index are one row |
| C. Seekable zstd files ([format](https://github.com/facebook/zstd/blob/dev/contrib/seekable_format/zstd_seekable_compression_format.md); Go library pre-v1) | as A | as A | as A |

S1 (`logs.db`, 1,389 one-hour sessions prefilled, 30 chunks/s):

| Chunk size | File | Ingest commit p99 | Disk written | Replay 1 h p99 | Offset lookup p99 | Delete 1 session | Prune 10 % | Vacuum |
|---|---|---|---|---|---|---|---|---|
| 2,048 B | 2.1 GB | 19.3 ms | 325 KB/s | 19.7 ms | 0.8 ms | 6.4 ms | 138 sessions, 0.94 s | 50,568 pages in 1.39 s, ≤ 66 ms per 2,000-page step |
| 650 B | 0.42 GB | 16.1 ms | 164 KB/s | 10.5 ms | 1.9 ms | 5.7 ms | 138 sessions, 0.99 s | 9,582 pages in 0.28 s |
| **3,300 B** (S2 measured) | 2.1 GB | 19.9 ms | 326 KB/s | 18.4 ms | 0.9 ms | 5.5 ms | 138 sessions, 0.85 s | 50,568 pages in 0.95 s |
| 3,300 B, Linux btrfs | 2.1 GB | 45.9 ms | 367 KB/s | 26.7 ms | 1.2 ms | 5.4 ms | 138 sessions, 0.48 s | 50,568 pages in 1.69 s |

At the 50 GiB cap (plan 0.15, 2026-10-08): S1 `logs` again, 2,048 B chunks,
on the Mac mini (M4 Pro, 64 GiB RAM, internal SSD, APFS), a shared host
(other agents running, load average 1.3 to 3.3), runs interleaved 2 GB /
50 GiB in one session; ranges over 3 runs at 2 GB and 2 at 50 GiB
([raw](../../spikes/sqlite/results/s1-logs-50g.txt)). Depth is read from the
file and matches SQLite's `dbstat` table.

| File | Prefill | Depth: table / indexes | Commit p99 | Replay 1 h p50 / p99 | Offset lookup p99 | Delete 1 session | Prune 10 % | Vacuum (step p99) |
|---|---|---|---|---|---|---|---|---|
| 2.1 GB, 0.5 M chunks | 7–10 s | 4 / 3 | 20–34 ms | 11–20 / 24–34 ms | 1.0–2.5 ms | 4.8–9.2 ms | 138 sessions, 0.85–1.29 s | 50,568 pages in 0.86–0.91 s (36–45 ms) |
| 53.6 GB (49.9 GiB), 12.9 M chunks | 4 m 42–45 s | 4 / 4 | 21–24 ms | 16–17 / 35–40 ms | 1.2–1.4 ms | 5.8–6.2 ms | 3,583 sessions, 21.6–22.0 s | 1,305,360 pages in 23–26 s (70–74 ms) |

The indexes gain one level and the table none. Commit, lookup and delete
showed no change beyond run-to-run spread; replay p99 rises by about 9 ms and vacuum step p99
nearly doubles. Single outliers were a 242 ms commit at 2 GB and a 344 ms vacuum step
at 50 GiB, both inside the 1 s batch window. Prune and vacuum time grow with
the amount removed. No block reads were counted in any run (`/usr/bin/time
-l`): the file was served from memory, so a cold cache is not measured. With
smaller chunks (Muse ≈ 650 B; Claude ≈ 470 B, 2026-10-12) hold several times
more rows, a shape not run at 50 GiB. Each 2,048 B chunk took a 4 KiB
page of its own (12.9 M chunks, 26.4 GB of chunk data, in a 53.6 GB file): a
chunk of about 2 to 4 KB costs one page, so the store on disk is larger than
the chunk bytes and the cap holds fewer days than the monthly volumes below
suggest.

**Decision: B.** A second SQLite file, `logs.db`, with its own writer
goroutine (the `logReceiver`'s writer), same pragmas as `state.db` plus
`auto_vacuum=INCREMENTAL`, one transaction per ≤ 1 s batch across all
streams; `LogAck` after commit. Retention measures the store as its live
pages, `(page_count − freelist_count) × page_size`, never the file size
(freed pages stay in the file until vacuumed) and never the sum of chunk
bytes. Over the cap it deletes the oldest `FINAL` sessions, one transaction
each, until under it, then runs `incremental_vacuum` in 2,000-page steps.
Each prune transaction and each vacuum step is one turn of the log writer,
taken between ingest batches, so a batch waits behind at most one of them
(at the cap: ≤ 36 ms and ≤ 344 ms). Per-session delete is one transaction and is audited in
`state.db`. Log I/O never queues behind state writes, and `state.db` backups
stay small (`logs.db` is backed up only with `fleetd backup --include-logs`).
The startup truncation of partial frames in 03d is no longer needed.

### Chunk framing and volume (S2)

[verified-facts](../research/verified-facts.md#measured-one-real-goal) measured 115× on one hour of Muse output compressed as a single stream.
S2 recorded one hour of a busy Codex goal on linux-box (2026-10-02 09:49–10:49
EDT, `tmux pipe-pane -o`, no keys sent; 17.8 MB raw) and compressed it the way
the agent would ship it (klauspost/compress `SpeedDefault` ≈ zstd level 3):

| Method | MB/h | Ratio | B/s per pane | Chunk p50 |
|---|---|---|---|---|
| whole hour, one frame (the verified-facts method) | 1.17 | 15× | 326 | — |
| independent frame per 10 s chunk | 2.18 | 8× | 605 | 5,459 B |
| independent frame per 60 s chunk | 1.47 | 12× | 408 | 23,654 B |
| 10 s chunks + dictionary trained on the first 20 % | 1.96 | 9× | 544 | 7,341 B |
| **one frame per 5 min, flushed every 10 s** | **1.25** | **14×** | **348** | **3,227 B** |
| one frame per 60 min, flushed every 10 s | 1.18 | 15× | 329 | 3,049 B |

Claude Code, one goal hour 2026-10-12 (Mac mini, 2.1.296, 120×40 pipe-pane; [raw deleted, full table kept](../../spikes/zstdchunk/results/s2-claude-mac-mini.txt)):
one scratch session (Go CLI from nothing, 7 prompts, 5 answers), 2.35 MB over 1.01 h.
Same shape as Codex: 5-minute framing **14×** (48 B/s, p50 470 B) vs 8× independent 10 s frames;
whole-stream 16× (40 B/s).

- A frame needs no end to be read: each flush emits complete zstd blocks, so
  a frame cut short by an agent crash still decodes up to its last block
  ([RFC 8878 §3.1](https://www.rfc-editor.org/rfc/rfc8878.html)).
- A dictionary helps little here; it pays off mainly in the first few KB of
  data ([zstd README](https://github.com/facebook/zstd)), and 10 s chunks are
  already larger.

**Decision.** The agent keeps one zstd frame per log stream open for at most
5 minutes or 4 MiB raw and flushes it into a chunk every 10 s; a crash, a gap
or the limit starts a new frame. Each chunk carries `frame_start` and
`frame_raw_offset` (contract change in
[ADR 0018](../adr/0018-hub-api-and-contract-amendments.md)); replay decodes from
the chunk that starts the frame, so seeking costs at most 5 minutes of
output (≈ 1.5 MB raw for Codex). This is within 7 % of the whole-hour ratio.

**Volume, corrected.** Busy panes write 65 B/s (Muse, verified-facts) to 348 B/s (Codex,
S2) after compression. Claude Code measured 40 B/s whole-stream (48 at 5-minute
framing) on 2026-10-12, below the span's floor; the budgeted span is unchanged until
the Muse leg lands and re-derives it (#60). The size cap therefore matters: **default
50 GiB**, oldest `FINAL` sessions pruned first, and the Usage view shows how many days
the cap currently holds. Chunks are ≈ 3.3 KB, which S1 measured above.

## Q7. Replay budget

At 100 messages per batch the writer committed 2,543–2,626 rows/s for 60 s
with commit p99 ≤ 28 ms and enqueue → commit p99 ≤ 59 ms, at most ≈ 70 % of
writer time. **Decision:** keep 200 ev/s per agent and 2,500 ev/s per hub
(decided here). Per-message Event jobs through the real writer at this
burst (row 5 ruling, fork 6) are measured in
[04c](04c-event-ingest-benchmark.md): 04c measured per-message jobs at
2,499-2,500 jobs/s, commit p99 36.8 ms, 42 % busy and enqueue → commit p99
62.4 ms, and set that line at 90 ms (fill plus the commit budget; arbiter
round 4).

**Hub scrubber cost (2026-10-10, row 5l).** The hub's secret scrubbing over
free text the agent already scrubbed is measured in
[04b](04b-hub-scrubber-cost.md): under 1 % of one core at the real rates, and
2.2 to 4.4 cores (19 to 37 % of 12) only if all 2,500 replayed events a second
carried a full 1 KiB message, which they cannot. **Decision:** no faster
first-pass scan before the first producer; the hub cuts or rejects a string
over its cap *before* scrubbing it (a 1 MiB hostile message takes 16 s);
row 6 pastes the real `CommandResult.message` size distribution and the hub
scrub cost before it merges, and row 17 re-checks.

## Q8. Provider limits and webhook ingress

- A GitHub App installation gets 5,000 REST requests/h, plus 50/h per repo and
  per org user beyond 20, up to 12,500 (15,000 on Enterprise Cloud)
  ([rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)).
  A50 polling is ≈ 1,300 req/h, 26 % of the base.
- A conditional request answered with 304 does not count against the primary
  limit when it is authorized
  ([best practices](https://docs.github.com/en/rest/using-the-rest-api/best-practices-for-using-the-rest-api)).
- GitHub does not redeliver failed webhook deliveries automatically; manual
  redelivery covers the past 3 days; a receiver must answer within 10 s
  ([failed deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries),
  [best practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks)).
  GitHub must be able to reach the URL (inferred from the docs' advice to use
  a forwarding proxy for local testing).

**Decision:** polling only in v1 (decided here). Webhook ingress is a
later, opt-in integration that still keeps the backstop poll.

## Q9. Agent-side persistence

**Decision:** `agent.db` in the agent state dir, same driver and pragmas as
the hub (`synchronous=FULL`, `fullfsync=ON`), one writer, transactions
batched ≤ 1 s. Tables: command log (written before `CommandAck`), event ring
(unique key per coalesced kind so the latest replaces older rows; size cap by
row count and bytes), spool chunks with `acked_offset` per stream. Raw
`pipe-pane` output goes to a plain append file per stream that the agent
compresses into chunks and truncates after each chunk commits. Agent rates
(≈ 0.1 ev/s, one chunk per pane per interval) are far below S1's.

Where each piece of agent state lives (the state dir is
`~/.local/state/fleet-agent/`):

| State | Home |
|---|---|
| Command log, event ring with `seq_hwm`, `EventCounters` and `install_id`, probe assignments, goal registry, availability marks (with `fresh_context_on_reuse`), drain state, effective settings and loaded policy hash, spool chunk index | `agent.db` |
| Key, certificate, CA chain and pinned fingerprint, `machine_id`, account salt, `fleet_id`, cached hub address (mode 0600, written before `enroll` returns) | plain files, so enrollment survives losing `agent.db` |
| `fleet-agent.lock`: a kernel `flock` (exclusive, non-blocking) that `fleet-agent run` takes before it opens `agent.db` and holds while it runs, so a second `run` on the same folder exits 1 (#92). The kernel drops it when the process ends; the file stays and holds the pid for a reader only | plain file, mode 0600 |
| Raw `pipe-pane` streams | plain append files (above) |
| `policy.yaml` | the owner's file in `~/.config/fleet-agent/` (`$XDG_CONFIG_HOME/fleet-agent/` when set to an absolute path; `--config-dir` overrides); the agent only reads it |
| Hook scripts | `~/.local/share/fleet-agent/hooks/` (01d) |

`bbolt` was considered: also one writer, but its file never shrinks without
compaction ([README](https://github.com/etcd-io/bbolt)) and it would be a
second engine to learn.

## UI build and serve

- Vite builds `web/` to `web/dist`; `fleetd` embeds it with `//go:embed
  all:dist` ([embed](https://pkg.go.dev/embed)) and serves it with
  `http.FileServerFS` (Go 1.22+, [net/http](https://pkg.go.dev/net/http#FileServerFS)).
- Unknown non-API paths return `index.html` (client routing). Hashed assets
  are served `Cache-Control: immutable`; `index.html` is `no-cache`.
- A placeholder `web/dist/index.html` is committed so `go build` works without
  Node; CI and `make` build the UI first.
- Development: the Vite dev server proxies Connect paths to a running
  `fleetd`. Node is needed to build, never to run.

## Rejected options

| Option | Why not |
|---|---|
| Postgres | Readers and writers never block each other under MVCC ([docs](https://www.postgresql.org/docs/current/mvcc-intro.html)), but SQLite already gives that at this load (S1), and Postgres is a server to install, upgrade and back up. Stays a future `store` implementation for more than one hub. |
| Redis | Pub/sub is at-most-once ([pubsub](https://redis.io/docs/latest/develop/pubsub/)); AOF `everysec` can lose ≈ 1 s ([persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)). Hot reads are fast enough from SQLite (Q4); fan-out is in process. |
| NATS JetStream (embedded) | Can run in process (`InProcessConn`, [nats-server](https://pkg.go.dev/github.com/nats-io/nats-server/v2/server)), but duplicates the ring and outbox durability, its file store syncs every 2 min by default (source), and it is a second store to back up. |
| SSE for live updates | Untyped second encoding beside the Connect contract; same connection limit without HTTP/2. |

## Not proven, and how it is covered

| Gap | Cover |
|---|---|
| `logs.db` at the 50 GiB cap with a cold page cache | Measured warm on 2026-10-08 (Q6, issue #60): indexes one level deeper, every single transaction and vacuum step within the 1 s batch window; no block reads were counted on the 64 GiB host. A cold run gates the merge of plan row 25, not its start: the `logs` scenario with a new spike flag, `-reuse`, that opens a prefilled `logs.db` and skips schema, vacuum and prefill, so prefill and measuring run as separate processes (prune and vacuum change the file, so each prefill gives one cold run); on a file larger than the machine's RAM (the hub MacBook preferred), or after the owner runs `sudo purge` by hand between the two processes, with the measuring process showing disk reads (`/usr/bin/time -l` block input operations in the thousands, not 0). Pass: commit p99 ≤ 250 ms; every commit, prune transaction and vacuum step under 1 s; replay 1 h p99 ≤ 1 s; offset lookup p99 ≤ 100 ms |
| Muse under the framing above (S2 measured Codex; Claude Code measured 2026-10-12: 14× at the 5-minute framing vs 8× for independent 10 s frames, same shape as Codex, Q6 table) | Re-run `zstdchunk` on one Muse goal hour; this measurement (issue #60) gates the v1 logs work, plan item 0.15 (section 3). Blocked 2026-10-12: the plan stands 98% used (6,888 / 7,000 AIC), so an hour-long recording would overspend the owner's quota — it needs a quota reset or top-up, or owner-designated live sessions |
| Harness transcripts (K3) compression | Measured in v3 before K3 ships. At A50 the raw transcript rate (1.08 MB/s) is higher than the terminal logs, so K3 stays opt-in and compressed |
