// Package inventory is fleet-agent's tmux inventory and lifecycle (01 E1,
// E2, E3; row 7 PR 7d).
//
// buildServer turns one read of a server (internal/agent/tmux
// Client.ReadInventory) into the TmuxServer the agent reports: names and
// commands bounded and control-stripped (02e), @fleet_* tags checked
// against their id's shape and otherwise dropped. diff turns two reads of
// the same server start into inventory events: SessionEvent, WindowEvent,
// PaneCreated, PaneChanged and PaneExited (exit_status only when tmux kept
// the pane).
//
// Sessions on the probe server, and sessions whose creator set the
// @fleet_internal user option, are internal (row-7 ruling fork 6); a tag is
// data anyone can set, and the hub trusts a session id only when its own
// table minted it (fork 7). The agent never reads a process environment.
package inventory
