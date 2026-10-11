// Package inventory is fleet-agent's tmux inventory and lifecycle (01 E1,
// E2, E3; row 7 PR 7d).
//
// buildServer turns one read of a server (internal/agent/tmux
// Client.ReadInventory) into the TmuxServer the agent reports: names and
// commands bounded and control-stripped (02e), @fleet_* tags checked
// against their id's shape and otherwise dropped. diff turns two reads of
// the same server start into inventory events: SessionEvent, WindowEvent,
// PaneCreated, PaneChanged and PaneExited (exit_status only when tmux kept
// the pane). The read's DeadSignal is not reported yet: no snapshot field
// carries it.
// An [Observer] watches the tmux socket folder ([SocketFolder]: $TMUX_TMPDIR
// or /tmp, then tmux-<uid>) with fsnotify and wakes a control client
// (internal/agent/tmux) when a server's socket appears: there is no timer
// (row-7 ruling fork 4). The folder is also re-listed at each Snapshot, each
// SnapshotRequest and after a control client's %exit. One sync goroutine per
// server reads the whole inventory with one list-panes -a whenever a
// control-mode notice, the structure subscription (a pane change in any
// session), a client state change or a Snapshot asks (notices at most once
// a second), turns it
// into the TmuxServer the agent reports (tmux sends each name, command and
// @fleet_* value over 1 KiB in bytes empty, and the read counts it; names
// and commands are then cut and control-stripped, 02e; @fleet_* tags are
// checked against their id's shape and otherwise dropped; the internal mark
// is tmux's exact compare with attach_view), and emits the difference from
// the last read as
// inventory events: TmuxServerEvent, SessionEvent, WindowEvent, PaneCreated,
// PaneChanged and PaneExited (exit_status only when tmux kept the pane).
// [Observer.Snapshot] is the conn Snapshot hook.
//
// Sessions on the probe server, and sessions whose @fleet_internal user
// option is exactly "attach_view" (tmux compares it; the agent never reads
// the value), are internal (row-7 ruling fork 6; ruling fork 2 on 7d1). A
// text field over 1 KiB reaches this package blank (tmux bounds it). A tag is
// data anyone can set, and the hub trusts a session id only when its own
// table minted it (fork 7). The agent never reads a process environment.
//
// The README has the concurrency diagram and the telemetry.
package inventory
