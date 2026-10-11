# internal/agent/tmux/inventory

The agent's tmux inventory and lifecycle (01 E1, E2, E3; row 7 PR 7d): which
tmux servers run, every session, window and pane on them, and the events
when they change.

```go
obs, err := inventory.New(inventory.Options{
	Program: tmuxPath, Runner: runner, Telemetry: tel, Emitter: emitter, // nil Emitter: Snapshot only
})                                   // servers "default" and "fleet-probe" in SocketFolder(...)
go obs.Run(ctx)                      // watch, one control client and one sync loop per server
err = obs.Snapshot(ctx, snap)        // the conn Snapshot hook: fills snap.TmuxServers
folder, off := obs.Folder(), obs.WatchOff() // status.json tmux_folder, tmux_watch
c := obs.Client("default")           // 7e and 7f subscribe through it
```

`Snapshot` needs `Run`: it asks each server's sync loop for a fresh read.

## Finding servers (row-7 ruling fork 4)

- The socket folder is tmux's own rule: `$TMUX_TMPDIR`, else `/tmp`, then
  `tmux-<uid>`; made 0700 when missing so it can be watched before the first
  server. A server started with `-S` elsewhere is not seen (01 E2).
- fsnotify watches the folder. An event naming `default` or `fleet-probe`
  pokes that server's waker: it calls the client's `Wake` at once, or once at
  the end of a 1 s gap after the last one (defaults), and never while the
  client is up, so the repeat `Create` kqueue can send for an existing socket
  is a no-op.
- The folder is re-listed at each Snapshot, each SnapshotRequest (the same
  hook) and after a control client's `%exit`. A watch that cannot be set, or
  whose folder vanishes and cannot be watched again, logs `tmux.watch_off`
  once; nothing polls.

## What is reported

- One `list-panes -a` reads everything (tmux runs one command at a time, so
  it is one moment): ids, indexes, flags, sizes, `pane_dead_status`,
  `pane_dead_signal` (as `PaneInfo.DeadSignal`, the closed set; no snapshot
  field carries it yet), the `@fleet_*` user options and the three names.
  Fields are split by a marker that is random per read, so no name can fake
  a field, and closed by one more after the last field; a newline tmux
  writes raw in an option value or a program's name is joined back. tmux
  bounds each name, command and option value at 1 KiB in bytes and sends a
  longer one empty, with its length: it reaches this package blank (counted
  in `fleet_agent_tmux_inventory_fields_dropped_total`), and never fails the
  read. A server that restarted while the command ran is refused (the start
  id is checked before and after).
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
- In the Snapshot, a server never seen is `running: false`; a server whose
  client is down keeps its last inventory with `observed: false`, so a pane
  is never reported gone because the observer was. A tmux older than 3.2
  is also reported `running: false` with no sessions: a guess, since the
  observer cannot read it; the real signal is the client's
  `tmux.exit{reason="too_old"}` and `REASON_CODE_TMUX_TOO_OLD` (03h).

## When reads fail

A read that fails (`command`, `reply`, `timeout` or `io`) changes nothing
the hub is told: the last good inventory stands, with `observed: true`,
because `observed` says the control client is attached (`snapshot.proto`
TmuxServer, 05b) and it still is. Nothing is reported gone or closed, so a
pane never looks exited because a read failed. The next notice, Snapshot,
SnapshotRequest or state change reads again, and the first good read emits
what changed meanwhile. The operator sees it in
`fleet_agent_tmux_inventory_reads_total{server_kind, result}` (a rise with
no `ok` beside it is a picture going stale) and in `tmux.inventory_failed`.
`timeout` comes only from a caller's deadline: the read path sets none of
its own today. A read the client refused as not attached, or that the
agent's stop cut short, is not counted (`fleet_agent_tmux_control_up` and
`tmux.exit` say why). A server whose every read has failed since the agent
started has no last inventory, so it is reported `running: false`, like
one never seen, while `fleet_agent_tmux_control_up` is 1.

## Events

The first state read after the agent starts emits nothing (the connect
Snapshot carries it). After that each read is compared with the last:

| Change | Event |
|---|---|
| a server appears, or restarts (new start id) | `TmuxServerEvent` CREATED with every session (after CLOSED for the old start) |
| no server answers on the socket | `TmuxServerEvent` CLOSED (ids only) |
| the client goes down, then comes back | `TmuxServerEvent` UPDATED `observed: false`; on return the diff, then UPDATED with the inventory (both carry every session: at 200 sessions about 200 KB of ring per blip, under the 960 KiB event cap; kept so each is a whole level) |
| a session or window appears | CREATED, with its windows and panes |
| renamed | RENAMED, its own fields |
| other attributes (clients, tags, auto rename, index; not focus) | UPDATED, its own fields |
| closed | CLOSED, ids only |
| a new pane id | `PaneCreated`; one first seen dead (it lived and died between two reads) also gets `PaneExited` `remains: true` |
| a pane's command, size, tags, window or index; a dead pane respawned | `PaneChanged` (a pane's or window's focus alone raises nothing) |
| a pane dies and tmux keeps it (`remain-on-exit`) | `PaneExited` with `exit_status` (unset for a signal death), `remains: true` |
| a pane is gone | `PaneExited` `remains: false`, `exit_status` unset unless it was read dead first |

Order: what is created (sessions, windows, panes), what changed, then what
went (panes, windows, sessions). Panes are keyed by pane id across the
server, so a grouped pair raises one event per pane. A pane read dead and
later dropped by tmux gets a second `PaneExited` (`remains: false`): the hub
applies it idempotently (02i), so the one-nudge rule runs once.

What starts a read (the seam is `triggers` in `trigger.go`): a window,
session or client notice, `%sessions-changed`, `%exit`, a new value of the
`fleet-structure` subscription, a client state change, and a Snapshot.
Notices and subscription values start a read at most once a second per
server (`readGap`, defaults); a Snapshot's read and a state change are not
paced. Measured on tmux 3.2a and 3.7c, a `-d` split, a kill of a non-active
pane and a pane death in a session the client is not attached to raise no
notice of their own, so the observer registers one subscription per server
(coordinator decision on 7d, amending row-7 ruling fork 5(f)):
`#{S:#{session_id}:#{W:#{window_id}=#{P:#{pane_id}.#{pane_dead} }}}`, which
tmux checks about once a second. A pane that lives under a second can be
missed by it and is read at the next rescan (Snapshot, SnapshotRequest, a
client state change). 7f measures the cost of the three subscriptions
together (E13's `session_activity`, E4's `window_activity` and this one;
proof 9).

## Concurrency

```text
Observer.Run (owns every goroutine below; returns when all have)
  watch goroutine: fsnotify events/errors, rescan chan (buffered 1)
     -> waker.poke: waker.mu around last/pending only; Client.Wake outside it;
        a trailing wake runs on a time.AfterFunc goroutine (same rules)
  per server: Client.Run (internal/agent/tmux: reader + repick goroutines)
     Notify on the reader goroutine: non-blocking sends only
       (dirty chan, buffered 1; on %exit also the rescan chan)
  per server: sync loop, the only goroutine that reads, diffs, stores and emits
     waits on dirty | Client.Changed() | snapshot request | ctx; after a
       notice it waits out readGap (a timer, still serving Snapshot requests)
     ReadInventory (one control-mode command) -> buildServer -> store last
       -> Emit each event (waits for the ring commit; no lock held)
  Snapshot hook (the conn session goroutine): a rescan token, then per
     server a request with a reply chan to its sync loop; no shared state
Locks: waker.mu only. The sync loop's state is its own; Client.mu is the
client's. Nothing is held across a control-mode command or an Emit.
```

## Telemetry

| Signal | What it says |
|---|---|
| `fleet_agent_tmux_servers{kind}` | 1 while a server of that kind runs and was read, else 0 |
| `tmux.server_seen` (`server_kind`, `server_start_id`, `version`) | a server start the agent had not seen |
| `tmux.watch_off` (`err_kind` = `constraint`, `permission`, `io`) | the folder watch is off; once per run |
| `fleet_agent_tmux_inventory_reads_total{server_kind, result}` | inventory reads that ended, `result` = `ok`, `command`, `reply`, `timeout`, `io` (closed); see "When reads fail" |
| `tmux.inventory_failed` (`server_kind`, `err_kind` = `command`, `reply`, `timeout`, `io`) | a read failed; at most once a minute per server |
| `fleet_agent_tmux_inventory_fields_dropped_total{field, reason}`, `tmux.inventory_field_dropped`, `fleet_agent_tmux_parse_errors_total`, `fleet_agent_tmux_lines_too_long_total` | internal/agent/tmux's parser signals: `New` builds one `tmux.Metrics` for every client it makes, so they count in production |
| `status.json` `tmux_folder`, `tmux_watch: off` | the folder watched, and whether the watch is off |

Event loss is the emitter's to count (`ring.emit_failed`, `evicted_total`).
Never a name, a command, a tag value or a path in a log line or label (05b).

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/inventory/`, against
real tmux 3.2 and the latest in `tmuxtest` servers (set `FLEET_TMUX_32`;
CI runs `./internal/agent/tmux/...`): a socket created in the watched folder
raises a connect, a repeat `Create` is a no-op, the watch off falls back to
rescans, lifecycle events, pane changes in a session the client is not on
(both versions), one read a second under a burst of notices, a restart, a
seeded tag, the probe server, events landing in a real ring, a field over
1 KiB counted through the observer's clients, and failed reads counted by
result while the last inventory stands; plus table
tests of the tree build, the diff, the trigger seam, the text and tag rules
and the wake debounce.
