// Package control runs the agent's pane-control commands (plan row 18,
// issue #67): F1 EnsureSession and F2 EnsureWindow here, the rest in later
// slices.
//
// A command arrives from the hub through the executor, which already checked
// the deadline, the epoch and seq, the safe mode, the rate limit, the role
// matrix and the local approval. Control checks what only it can see: the
// call's own shape, the drain state, the local policy's repo roots, and tmux
// itself. It drives tmux two ways: one-shot CLI calls through internal/proc
// (the "tmux" caller; sessions and windows are created, never polled) and,
// from the SendKeys slice on, the observer client of internal/agent/tmux.
// It never imports os/exec directly (the depguard rule names who may).
package control
