# internal/agent/logship

K1 pipe-pane capture of goal panes into the agent's local raw files (plan
row 21, issue #70). `StartGoal` step 7 starts the capture after the goal
registers; the raw bytes feed row 25 (frames, spool, shipping, `logs.db`).

- `logship.go`: `Tracker` against one tmux server. `Start` pre-creates
  `<state dir>/raw/<server>/<digits>.log` mode 0600 and runs
  `pipe-pane -o -t <pane> cat >> <path>`; a second start re-points the
  same file, so a restart appends. `Stop` closes the pane's pipe; stopping
  an unknown pane is OK. `Captured` / `Active` are the read seam for the
  future `TmuxPane.log_capture` level (the agent connection row wires it
  into the inventory). The pane id loses its `%` in the file name
  (`%3` maps to `3.log`): tmux expands `%`-sequences in the pipe command.
  The path is tmux-escaped (`%` to `%%`, `#` to `##`) and then
  single-quoted, because tmux expands status-left formats (`#{...}`,
  `#W`) in the command; both escapes are proven live on tmux 3.8.
- `frame.go`: `Chunker` frames one stream's raw bytes into chunks (03d W9:
  the frame closes after 5 min or 4 MiB raw, flushed every 10 s or 256 KiB
  raw). One zstd frame per stream; a chunk is the frame's blocks since the
  last flush, so replay decodes from the chunk at `frame_raw_offset`.
  `Write` returns only flushed chunks; pending bytes ship on a later
  `Flush` or `Finalize`.
- `spool.go`: `Spool` is one stream's durable outbox (K2):
  `spool/<server>/<start>/<pane>/<stream>/` holds one `.chk` file per
  chunk (JSON header line, then the zstd data) plus the durable `acked`
  offset. Chunks stay until the hub acks them; the cap drops the oldest
  and the floor advances, so the next chunk carries the gap. `Pause` /
  `Resume` hold shipping while capture continues.
- Seams: `Runner` (`*proc.Runner` satisfies it, caller `tmux`), so unit
  tests prove the exact argv and live tests prove the bytes.

The hub never names a path: it learns the capture is on through the
snapshot level. The `SessionLog` stream client feeding the spool (the
tailer wiring the raw files to the `Chunker`, acks, `HOLE` resends and the
`SetLogShipping` pause) lands with the agent connection row.

Tests: `CGO_ENABLED=0 go test -count=3 ./internal/agent/logship/` (unit
plus live capture against the `tmuxtest` server; the 3.2 leg runs under
`FLEET_TMUX_32`). No harness needed.
