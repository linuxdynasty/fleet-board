# 04. Technology review

- Status: Accepted
- Date: 2026-10-02
- Inputs: [03g](03g-load-and-handoff.md) (load table, questions Q1–Q9),
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

| Question (03g) | Decision | Proven by |
|---|---|---|
| Q1 durable store | **SQLite stays.** `modernc.org/sqlite`, WAL, `synchronous=FULL`, `fullfsync=ON`, one writer, 250 ms / 100-message batches | S1: 2,543 rows/s for 60 s, commit p99 ≤ 28 ms, readers never blocked |
| Q2 pane summaries | Every update persisted, latest per pane (ADR 0013); the final summary of each pane moves to `logs.db` when its session ends | S1: 100 updates/s costs 0.79 MB/s of disk writes at A50 |
| Q3 queue or broker | **None.** In-process bounded channels as in 03f | Load is 110 msg/s; at-least-once already comes from the agent ring and the outbox |
| Q4 hot state | **Read from the store.** No cache process, no in-memory copy of record | S1: 500-row sidebar read p99 21 ms under burst |
| Q5 browser fan-out | **Connect** (unary + server streaming, one stream per tab); terminal attach over **WebSocket** with byte acks | Vendor docs (browsers cannot stream requests) |
| Q6 log storage | **`logs.db`**: zstd chunks as rows in a second SQLite file with its own writer; replaces "files on disk" in ADR 0010 | S1: 30 chunks/s commit p99 ≤ 19 ms; delete one session 6 ms; prune and vacuum in ≈ 55 ms steps |
| Q6 chunk framing | One zstd frame per stream for ≤ 5 min or 4 MiB raw, flushed into a chunk every 10 s; replay seeks by frame | S2: 14× vs 8× for independent 10 s frames (Codex, 60 min) |
| Q6 log volume | 03g was low: Codex output compresses 15×, not 115× (Muse); A50 is **50–270 GB/month** depending on the harness mix; default size cap 50 GiB | S2 |
| Q7 replay budget | 200 ev/s per agent, 2,500 ev/s per hub, as proposed | S1 burst |
| Q8 provider limits | Polling only in v1; webhook ingress later and optional | GitHub docs: 5,000 req/h per installation; 304s free |
| Q9 agent persistence | `agent.db` (same driver) for command log, event ring and spool chunks; raw `pipe-pane` output in plain append files | Same engine as S1; agent rates ≤ 1 tx/s |
| UI build and serve | Vite → `web/dist` → `go:embed` → `http.FileServerFS`; Node only at build time | Go docs |
| Redis, Postgres, NATS | **Not used** | See [Rejected](#rejected-options) |

## Q1. Durable store

**Requirement (03g).** One writer, ≈ 52 rows/s steady and ≈ 2,500 rows/s for
60 s at A50; ≤ 10 browsers reading without blocking the writer; zero-dependency
install; a laptop hub that can lose power.

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

Linux (omarchy, x86_64, btrfs, load average 15 from a running Codex goal,
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

03g asks for ≤ 10 reads/s of ≈ 500 rows under 100 ms. S1 measured the
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

**Decision: B.** A second SQLite file, `logs.db`, with its own writer
goroutine (the `logReceiver`'s writer), same pragmas as `state.db` plus
`auto_vacuum=INCREMENTAL`, one transaction per ≤ 1 s batch across all
streams; `LogAck` after commit. Retention deletes the oldest `FINAL`
sessions until under the size cap, then runs `incremental_vacuum` in
2,000-page steps. Per-session delete is one transaction and is audited in
`state.db`. Log I/O never queues behind state writes, and `state.db` backups
stay small (`logs.db` is backed up only with `fleetd backup --include-logs`).
The startup truncation of partial frames in 03d is no longer needed.

### Chunk framing and volume (S2)

01c measured 115× on one hour of Muse output compressed as a single stream.
S2 recorded one hour of a busy Codex goal on omarchy (2026-10-02 09:49–10:49
EDT, `tmux pipe-pane -o`, no keys sent; 17.8 MB raw) and compressed it the way
the agent would ship it (klauspost/compress `SpeedDefault` ≈ zstd level 3):

| Method | MB/h | Ratio | B/s per pane | Chunk p50 |
|---|---|---|---|---|
| whole hour, one frame (01c's method) | 1.17 | 15× | 326 | — |
| independent frame per 10 s chunk | 2.18 | 8× | 605 | 5,459 B |
| independent frame per 60 s chunk | 1.47 | 12× | 408 | 23,654 B |
| 10 s chunks + dictionary trained on the first 20 % | 1.96 | 9× | 544 | 7,341 B |
| **one frame per 5 min, flushed every 10 s** | **1.25** | **14×** | **348** | **3,227 B** |
| one frame per 60 min, flushed every 10 s | 1.18 | 15× | 329 | 3,049 B |

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

**Volume, corrected.** Busy panes write 65 B/s (Muse, 01c) to 348 B/s (Codex,
S2) after compression. At A50 with 300 capturing panes that is 19.5–104 KB/s,
**50–270 GB/month**; at A4 (10 panes) 2–9 GB/month. The size cap therefore
matters: **default 50 GiB**, oldest `FINAL` sessions pruned first, and the
Usage view shows how many days the cap currently holds. Chunks are ≈ 3.3 KB,
which S1 measured above.

## Q7. Replay budget

At 100 messages per batch the writer committed 2,543–2,626 rows/s for 60 s
with commit p99 ≤ 28 ms and enqueue → commit p99 ≤ 59 ms, at most ≈ 70 % of
writer time. **Decision:** keep 200 ev/s per agent and 2,500 ev/s per hub
(03g open question 14).

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

**Decision:** polling only in v1 (03g open question 13). Webhook ingress is a
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
| Raw `pipe-pane` streams | plain append files (above) |
| `policy.yaml` | the owner's file in `~/.config/fleet-agent/`; the agent only reads it |
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
| `logs.db` at the 50 GiB default cap (S1 used 2 GB) | B-tree depth grows by about one level; re-run S1 `logs` with a larger prefill before the first logs PR (logs are v2: the v1 plan, section 9) |
| Muse and Claude Code under the framing above (S2 measured Codex) | Re-run `zstdchunk` on one goal hour of each before the first logs PR (v2, plan section 9) |
| Harness transcripts (K3) compression | Measured in v3 before K3 ships (03g open question 11) |
