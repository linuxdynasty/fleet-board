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
// expands status-left formats in it. The raw file holds bytes only: zstd
// frames, the spool, shipping, logs.db and the 0.15 sizing stay row 25,
// and the hub's SetLogShipping pause belongs to that row too.
//
// Product code never runs tmux directly: one-shot calls go through
// internal/proc (the "tmux" caller), behind the Runner seam, so tests run
// the same argv against a throwaway server.
package logship
