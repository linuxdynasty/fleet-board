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
// (internal/agent/tmux) when a server's socket appears: the folder watch
// itself never polls (row-7 ruling fork 4). The folder is also re-listed at each Snapshot, each
// SnapshotRequest and after a control client's %exit. One sync goroutine per
// server reads the whole inventory with one list-panes -a whenever a
// control-mode notice, the structure subscription (a pane change in any
// session), a client state change, a Snapshot, or a follow-up owed to a
// status-less remains death asks (notices at most once a second; the one
// bounded timer arms a follow-up a second after the last read until the
// status lands or deadStatusPollMax reads run out), turns it
// into the TmuxServer the agent reports (tmux sends each name, command and
// @fleet_* value over 1 KiB in bytes empty, and the read counts it; names
// and commands are then cut and control-stripped, 02e; @fleet_* tags are
// checked against their id's shape and otherwise dropped; the internal mark
// is tmux's exact compare with attach_view), and emits the difference from
// the last read as
// inventory events: TmuxServerEvent, SessionEvent, WindowEvent, PaneCreated,
// PaneChanged and PaneExited (exit_status only when tmux kept the pane).
// Each read is bounded at 10 s (defaults, fixed): past it the read is
// abandoned and counted as a timeout, keeping the last good inventory. The
// first good read after reads failed emits CREATED with the full server;
// a clean start emits nothing.
// E13 developer input (01 E13, 02h) comes from the fleet-input
// session-activity subscription and one list-clients read per change: the
// inputTracker credits a moved or first-seen client at its pane (control
// clients ignored) and emits PaneInput, at most one per pane per 10 s,
// while TmuxPane.last_input_time keeps every key. Until the subscription
// confirms, a 2 s backup poll keeps the same read going (ADR 0024,
// decision 9); [Observer.CheckInput] is row 18's on-demand read for the
// SendKeys check: it reads at that moment and stamps, never emits.
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
