# Step 4 spikes

Throwaway measurement code for
[04-technology-review.md](../docs/specs/04-technology-review.md). Not part of
`fleetd`, `fleet-agent` or `fleet`; each spike is its own Go module so nothing
here becomes a dependency of the product. Raw output is kept under
`*/results/` so every number in the spec can be traced.

| Spike | Question | Run |
|---|---|---|
| [S1 `sqlite/`](sqlite/) | Does embedded SQLite with one writer meet 04's A50 load on a pure-Go driver, is a commit durable on macOS, and does `logs.db` hold session logs? | `go build -o s1 . && ./s1 -driver modernc -scenario commit\|steady\|burst\|logs` |
| [S2 `zstdchunk/`](zstdchunk/) | How well does zstd compress real terminal output when shipped in 10 s chunks rather than one stream? | `tmux pipe-pane -o -t <pane> 'zstdchunk rec <prefix>'`, then `zstdchunk analyze <prefix>` |
| [0.5 `tmuxtyping/`](tmuxtyping/results/README.md) | Can tmux push "a person typed" to a control-mode client, which release added `pause-after` and `ignore-size`, and how is tmux 3.2 installed on CI? (plan item 0.5, #50) | `go build -o tmuxtyping . && ./tmuxtyping typing\|flags -tmux <bin> -L fleetprobe-<name>`, then `./tmuxtyping summarize <file>...` |
| [0.7 `mdns/`](mdns/results/README.md) | Does `hashicorp/mdns` advertise `_fleetd._tcp` next to macOS mDNSResponder, and what browse timeout and re-query interval should the agent use? (plan item 0.7, #52) | `go build -o mdns . && ./mdns advertise\|browse\|summarize` |

## S1 setup

- Machines: the hub machine (Linuxdynasty-MacBook-Pro-14, Apple silicon,
  internal SSD, APFS) and omarchy (Linux 7.2, x86_64, btrfs, while a Codex
  goal was running), Go 1.27.1, 2026-10-02. The Linux binary is built with
  `GOOS=linux GOARCH=amd64 CGO_ENABLED=0`; disk bytes come from
  `/proc/self/io` there.
- Drivers: `modernc.org/sqlite` v1.60.1 and `github.com/ncruces/go-sqlite3`
  v0.35.6, both SQLite 3.53.4.
- Pragmas: `journal_mode=WAL`, `synchronous=FULL`, `fullfsync=1` (unless the
  row says otherwise), `busy_timeout=5000`, `journal_size_limit=64 MiB`;
  writer `_txlock=immediate`, one connection; readers `query_only`, pool of 10.
- Load (`steady`): pane summaries 100/s × 2 KB overwritten in place over 500
  panes, never-dropped events 7/s × 0.5 KB, other rows 20/s × 0.5 KB; 10
  browsers each reading the 500-row sidebar, 30 visible summaries and a
  50-event timeline once a second. `burst` adds 2,500 events/s (the hub-wide
  replay budget). Data is random printable bytes (pessimistic for page reuse).
- `logs`: `logs.db` with `auto_vacuum=INCREMENTAL`, prefilled with 500,000
  chunks (1,389 one-hour sessions), then 30 chunks/s committed once a second
  while 10 readers replay a whole session and look up an offset every 2 s;
  then one per-session delete, pruning of the oldest 10 %, and
  `incremental_vacuum(2000)` steps until the freelist is empty.
- `logs` at the 50 GiB cap (plan 0.15, 2026-10-08): the Mac mini (M4 Pro,
  64 GiB RAM, internal SSD, APFS), `-prefill 12900000` (49.9 GiB) against
  `-prefill 500000` in the same session; the run prints each B-tree's depth
  ([`results/s1-logs-50g.txt`](sqlite/results/s1-logs-50g.txt)).
- Disk bytes written come from macOS `proc_pid_rusage`
  (`ri_diskio_byteswritten`), so they include WAL, checkpoints and growth.

## S2 setup

- Recorder attached with `tmux pipe-pane -o` (no keys sent) to a busy Codex
  goal pane on omarchy (Linux x86_64), 60 minutes from 2026-10-02 09:49 EDT.
- Only the analysis output is kept (`zstdchunk/results/`); the raw recording
  was deleted from omarchy and from this machine after analysis because
  terminal output can contain secrets.
- Compression with `github.com/klauspost/compress/zstd` v1.20.1 at
  `SpeedDefault` (≈ zstd level 3), the encoder the agent would use; the
  dictionary is trained with the zstd 1.5.7 CLI.
