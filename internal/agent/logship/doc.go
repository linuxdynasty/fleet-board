// Package logship captures a goal pane's terminal output into the agent's
// local raw files (plan row 21, issue #70, K1). StartGoal step 7 starts the
// capture right after the goal registers; the hub never names a path, it
// only learns the capture is on through TmuxPane.log_capture.
//
// A capture is one `tmux pipe-pane -o` appending the pane to a plain file
// per stream (`<state dir>/raw/<server>/<digits>.log`, 04 Q9), mode 0600,
// so a restart appends to the same file. The pane id loses its `%` in the
// file name (`%3` maps to `3.log`); the path in the pipe command is
// tmux-escaped (`%` to `%%`, `#` to `##`) and single-quoted, because tmux
// expands status-left formats in it. The raw file holds bytes only: the
// Chunker frames one stream's raw bytes into zstd chunks and the Spool
// durables them until the hub acks each one (row 25); the SessionLog
// stream client that drains the spool belongs to the agent connection row.
//
// Product code never runs tmux directly: one-shot calls go through
// internal/proc (the "tmux" caller), behind the Runner seam, so tests run
// the same argv against a throwaway server.
package logship
