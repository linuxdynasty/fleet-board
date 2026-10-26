# internal/agent/logship

K1 pipe-pane capture of goal panes into the agent's local raw files (plan
row 21, issue #70). `StartGoal` step 7 starts the capture after the goal
registers; the raw bytes feed row 25 (frames, spool, shipping, `logs.db`).

- `logship.go`: `Tracker` against one tmux server. `Start` pre-creates
  `<state dir>/raw/<server>/<digits>.log` mode 0600 and runs
  `pipe-pane -o -t <pane> cat >> <path>`; a second start re-points the
  same file, so a restart appends. `Stop` closes the pane's pipe; stopping
  an unknown pane is OK. `Captured` / `Active` are the read seam for the
  future `TmuxPane.log_capture` level (row 25 wires it into the
  inventory). The pane id loses its `%` in the file name (`%3` maps to
  `3.log`): tmux expands `%`-sequences in the pipe command. The path is
  tmux-escaped (`%` to `%%`, `#` to `##`) and then single-quoted, because
  tmux expands status-left formats (`#{...}`, `#W`) in the command; both
  escapes are proven live on tmux 3.8.
- Seams: `Runner` (`*proc.Runner` satisfies it, caller `tmux`), so unit
  tests prove the exact argv and live tests prove the bytes.

The hub never names a path: it learns the capture is on through the
snapshot level. Shipping, the spool, `logs.db`, the 0.15 sizing and the
`SetLogShipping` pause are row 25 and stay out of this package.

Tests: `CGO_ENABLED=0 go test -count=3 ./internal/agent/logship/` (unit
plus live capture against the `tmuxtest` server; the 3.2 leg runs under
`FLEET_TMUX_32`). No harness needed.
