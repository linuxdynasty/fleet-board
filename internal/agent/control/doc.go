// Package control runs the agent's pane-control commands (plan row 18,
// issue #67): F1 EnsureSession, F2 EnsureWindow, F3 SendKeys
// (OPERATOR_TEXT only), F4 Interrupt, F5 RespawnPane (the pane's own
// command only), F6 KillWindow, F8 RunHarnessCommand and E11
// SetPaneAvailability here, the rest in later slices. Production registers
// only F4 for the default server;
// its capability is tmux.default_server.interrupt version 1.
//
// A command arrives from the hub through the executor, which already checked
// the deadline, the epoch and seq, the safe mode, the rate limit, the role
// matrix and the local approval. Control checks what only it can see: the
// call's own shape, the drain state, the local policy's repo roots, and tmux
// itself. It drives tmux two ways: one-shot CLI calls through internal/proc
// (the "tmux" caller; sessions and windows are created, never polled) and
// the observer client of internal/agent/tmux, which carries the SendKeys
// lines and the F4 interrupt's fixed C-c with an atomic server-start
// check. It never imports os/exec directly (the depguard rule names who
// may).
// Interrupt, SendKeys, and RespawnPane share a pane lock for each live call;
// the last holder or waiter removes it after the pane operation ends.
package control
