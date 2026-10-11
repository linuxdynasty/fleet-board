# internal/agent/tmux/inventory

The agent's tmux inventory and lifecycle (01 E1, E2, E3; row 7 PR 7d): every
session, window and pane on a tmux server as the agent reports it, and the
events when they change.

## What is reported

- One `list-panes -a` reads everything (tmux runs one command at a time, so
  it is one moment): ids, indexes, flags, sizes, `pane_dead_status`, the
  `@fleet_*` user options and the three names. Fields are split by a marker
  that is random per read, so no name can fake a field, and closed by one
  more after the last field; a newline tmux writes raw in an option value or
  a program's name is joined back. tmux bounds each name,
  command and option value at 1 KiB in bytes and sends a longer one empty,
  with its length: it reaches this package blank (counted in
  `fleet_agent_tmux_inventory_fields_dropped_total`), and never fails the
  read. A server that restarted
  while the command ran is refused (the start id is checked before and
  after).
- Session and window names and `current_command` are cut to 256 bytes on a
  rune boundary with every control character and invalid UTF-8 dropped
  (02e). They are never logged.
- Tags: `@fleet_session_id`, `@fleet_owner`, `@fleet_pane_key` and
  `@fleet_goal_id` (fork 7), as a format finds them from each pane (pane,
  window, then session options), reported under their `FLEET_*` names only
  when they have their id's shape, else dropped whole. A session with a
  `FLEET_SESSION_ID` is `started_by_fleet`, and a pane whose
  `FLEET_PANE_KEY` is its own key (`server/server_start_id/pane_id`; one
  inherited from a session option names another pane) is. A tag is data anyone can set: the hub trusts a session id only when
  its own table minted it. No process environment is ever read. A session's
  tags are taken from its first pane, through tmux's inheritance, so a pane
  or window option of the same name set by hand shadows the session's.
- Internal (fork 6): every session on the probe server, and a session whose
  `@fleet_internal` is exactly `attach_view` (also `attach_view` then; tmux
  compares the mark, the agent never reads it; any other value, such as
  `attach_view` and a newline, is a developer session: ruling fork 2 on
  7d1). Grouping
  alone never makes a session internal: a developer's grouped pair is two
  sessions with the same PaneKeys.
- `harness` unset, `availability` not idle and not available, and
  `ready_pattern_showing` false: rows 8 and 18 own them (fork 9). `activity`
  and output times are 7e's, input time 7f's, `workdir` and `goal` later
  rows'.
- `attached_clients` leaves out the agent's own control client.

## Events

`diff` compares two reads of the same server start:

| Change | Event |
|---|---|
| a session or window appears | CREATED, with its windows and panes |
| renamed | RENAMED, its own fields |
| other attributes (clients, tags, auto rename, index) | UPDATED, its own fields |
| closed | CLOSED, ids only |
| a new pane id | `PaneCreated`; one first seen dead (it lived and died between two reads) also gets `PaneExited` `remains: true` |
| a pane's command, size, tags, window or index; a dead pane respawned | `PaneChanged` (focus alone raises nothing) |
| a pane dies and tmux keeps it (`remain-on-exit`) | `PaneExited` with `exit_status` (unset for a signal death), `remains: true` |
| a pane is gone | `PaneExited` `remains: false`, `exit_status` unset unless it was read dead first |

Order: what is created (sessions, windows, panes), what changed, then what
went (panes, windows, sessions). Panes are keyed by pane id across the
server, so a grouped pair raises one event per pane. A pane read dead and
later dropped by tmux gets a second `PaneExited` (`remains: false`): the hub
applies it idempotently (02i), so the one-nudge rule runs once.

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/inventory/`: table tests
of the tree build, the diff, and the text and tag rules.
