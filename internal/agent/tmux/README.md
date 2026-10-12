# internal/agent/tmux

The agent's tmux control-mode code (01 E2): the protocol parser (7c1) and
the `tmux -C` client that attaches to one server and sends typed commands
(7c2). The test rig that starts throwaway servers is
[`tmuxtest`](tmuxtest/README.md).

- `NewParser(r, Options)` and `Parser.Next()` read a control client's output
  one message at a time: `Reply` (a `%begin` block and its `%end` or
  `%error`), `Output` (`%output` and `%extended-output`, decoded), `Pause`,
  `Continue`, `Exit`, `SubscriptionChanged`, `WindowAdd` (`%window-add`,
  `%unlinked-window-add`), `WindowClose` (`%unlinked-window-close`; the linked
  `%window-close` is not read yet), `WindowRenamed`, `SessionChanged`,
  `SessionsChanged` (a session was created or destroyed), `Unknown` (name only) and `Discarded` (an error class and a byte count).
  Window and session names are the developer's text: never logged.
- Inside a reply only the exact closing guard ends it and nothing is lifted
  out: command output can be pane text that starts with `%`. tmux writes
  `%pause %N` and `%continue %N` inside the reply to the client's own
  `refresh-client -A`; `Reply.ControlNotes()` finds those lines. The client
  may honour a `%pause` from any reply (pausing is the safe direction),
  but a `%continue` only from its own `refresh-client -A` reply for that
  pane, never marking a pane resumed on a `%continue` it did not ask for (the
  client below does exactly this).
- Bounds: `MaxLineBytes` (1 MiB) and `MaxReplyBytes` (8 MiB), in
  [defaults](../../../docs/specs/defaults.md). A longer line is skipped to its
  newline; memory never grows with a line's length. Each kept reply line is
  charged its bytes plus `ReplyLineOverhead` (17), so a reply of empty lines
  is bounded too. Options are per Parser; with the defaults a scrollback
  capture of more than about 86,000 full 80-column lines (8 MiB at 97 bytes
  a line) is cut short and flagged by `Reply.Dropped` (scrollback history
  goes through `LOGS` and `pipe-pane` per the spec, not a capture reply).
- `NewMetrics(tel)` counts failures in `fleet_agent_tmux_parse_errors_total{err_kind}`
  and `fleet_agent_tmux_lines_too_long_total`, and logs `tmux.parse_error` and
  `tmux.line_too_long` at most once a minute per class (05b). Given to a
  client as `ClientOptions.Parser.Metrics`, it also counts the text fields
  `ReadInventory` left blank (below).

## The client

```go
c, err := tmux.NewClient(tmux.ClientOptions{
	Program: tmuxPath, Socket: socket, Server: "default", // no default: cmd builds the target
	Runner: runner, Telemetry: tel, Notify: handoff,     // Notify must not block or call c
})
go c.Run(ctx)                                    // attaches, re-attaches, waits when absent
c.Wake()                                         // 7d: the socket folder changed
err = c.Subscribe(ctx, tmux.Subscription{Name: "win-act", Format: "#{S:#{W:#{window_id}=#{window_activity} }}"})
err = c.Send(ctx, line)                          // a typing.Line only
cap, err := c.Capture(ctx, pane)                 // capture-pane -p
panes, err := c.ListPanes(ctx)                   // list-panes -a: ids and numbers only
inv, err := c.ReadInventory(ctx)                 // list-panes -a: names, flags, @fleet_* options too
st, changed := c.Changed()                       // the state, and a chan closed at its next change
```

- `ReadInventory` reads every session, window and pane with one
  `list-panes -a` for the inventory package (7d): fields split by a marker
  random per read (no name can fake a field), a record that a newline in a
  value broke (in any text field, the last one too) joined back by counting
  markers, the start id checked before
  and after (a restart meanwhile is `ErrNotAttached`), the agent's own
  control client left out of a session's client count, one pane per session
  (a grouped pair lists a pane twice; a window linked twice into one session
  is kept at its first index). Names, commands and option values are raw and
  untrusted: the caller bounds and checks them.
- Each record is the ids, flags and sizes (with `pane_dead_status` and
  `pane_dead_signal`, read by the `ListPanes` rule below and its shared
  helper: `DeadSignal` only for a dead pane, a status and a signal both
  present is `ErrBadReply`); `#{==:#{@fleet_internal},attach_view}`
  (1 only for that exact mark: `Tags.AttachView`; the value never leaves
  tmux); the byte length `#{n:X}` of each of the seven text fields
  (`@fleet_session_id`, `@fleet_owner`, `@fleet_pane_key`, `@fleet_goal_id`,
  `session_name`, `window_name`, `pane_current_command`); a canary
  `#{e|<:1,2}` that must read `1` (a tmux without these modifiers is
  `ErrBadReply`, never a read of blank fields); then each text field as
  `#{?#{e|<:#{n:X},1025},#{X},}`; then a closing marker, so a newline in the
  last field (`pane_current_command`: a program's name can hold one) still
  leaves the record short of markers and the next line is joined to it. So tmux bounds every text field in bytes
  at `MaxFieldBytes` (1 KiB, defaults) and sends a longer value empty
  (`e|<` compares numbers; a plain `<` compares strings). `#{=N:...}` is no
  bound: it cuts by screen width, and zero-width characters and `#[style]`
  blocks pass it whole. The agent checks each field against its length: at
  most 1 KiB must arrive whole, over it empty; any other shape blanks that
  field only (`mismatch`). Either way the field reads as empty and is
  counted; no text value can fail the read. The length also checks the join
  of a record a newline broke. Without `-u` (below), under LANG=C tmux
  3.2a and 3.7c send each non-ASCII or control character as `_` or drop
  it; the length check sees a change in byte count only (`a\nb` sent as
  `a_b` has the same length), which is why the client runs with `-u`.
- What the bound leaves: one known gap, the 8 MiB reply bound. Each reply
  line is charged its bytes plus 17, so a newline in a value costs 17
  bytes, not 1. Measured on 3.2a and 3.7c (macOS), with `pane_dead_signal`
  in the record (it adds 18 bytes, a marker and an empty value):
  - Normal panes (short names, no tags): 623 bytes a record (622 on 3.7c),
    640 charged: about 13,100 panes on one server.
  - Long values: every text field at 1 KiB with no newline is 7,798 bytes,
    7,815 charged: about 1,073 panes (6,777 bytes measured with a 3-byte
    command, plus the rest of the command's 1 KiB).
  - Newline-heavy, the worst case: the four `@fleet_*` options set with
    `set-option -g` to 1,024 newlines each make every pane's record 4,097
    lines, 70,284 bytes charged; 119 panes read, 120 fail. Session and
    window names cannot add lines (3.2a escapes a newline in them, 3.7c
    refuses one). A program's name can (the last field); with 1 KiB of
    newlines there too it would be about 95 panes (computed, not measured).

  Past the bound the read fails loudly, never partly: `ErrBadReply`,
  counted in `fleet_agent_tmux_parse_errors_total{err_kind="reply_too_large"}`
  (measured: 1 at 120 panes) and 7d2's `tmux.inventory_failed{err_kind=reply}`.
  Every case is far above the budget in
  [04](../../../docs/specs/04-technology-review.md): 10 observed panes per
  machine and 500 fleet-wide.
- `Run` checks `tmux -V` (below 3.2: `ErrTooOld`, REASON_CODE_TMUX_TOO_OLD),
  lists sessions with one `tmux list-sessions` through `internal/proc` and
  attaches by one rule on every server: the lowest session not marked
  internal, else the lowest internal one (no session at all is absent).
  The mark is a session option (`set-option -t <session>`), which every
  pane of the session inherits: this list reads it from the current
  window's active pane and `ReadInventory` takes a session's tags from its
  first pane, so they agree (`TestSessionMarksAgreeBetweenThePickAndTheInventory`).
  A mark set on one window or pane only is not a session mark, and the two
  may differ.
  A session is internal only when its `@fleet_internal` is exactly
  `attach_view`: the list reads `#{session_id} #{==:#{@fleet_internal},attach_view}`,
  so tmux compares the mark and each line is `$N 0` or `$N 1` (anything else
  is an `attach_failed` `list`; a mark with a newline once put a bare line
  there and failed every attach). It attaches as `tmux -u -S <socket> -C
  attach-session -E -t $N -f ignore-size,pause-after=1` (`-u`: the control
  client runs as UTF-8, so names and values are byte-exact whatever LANG
  the agent has; `-E`: tmux does not apply `update-environment` to the
  developer's session). Its first command reads
  `#{client_flags}|#{pid}|#{start_time}|#{version}`: both flags missing from
  `client_flags` is `ErrTooOld` (never `-F`, spike 0.5 finding 8); the pid
  and start time make `ServerStartID`. Then every registered subscription is
  sent; one tmux refuses is an `attach_failed` (`err_kind` `subscribe`), not
  up, and `Subscribe` drops one refused while up. On an internal session
  the client re-picks once (below), then the state is `StateUp`: an
  internal session is a full observer.
- Re-pick (arbiter ruling on 7c2b): while on an internal session, once
  before up and on every `%sessions-changed`, the client runs `list-sessions`
  through the control client and, when a developer session exists, `switch-client -E -t $N`
  to the lowest one (`-E` keeps the agent's environment out of it;
  `%session-changed` confirms and `Session()` reports it; subscriptions
  survive). A burst of notices coalesces into one list in flight. A failed
  list or refused switch logs `tmux.repick_failed` (at most once a minute)
  and waits for the next notice; it never detaches (the conn ending during
  a re-pick is not logged as one). On a developer session
  `%sessions-changed` does nothing. A `%session-changed` the client did not
  ask for (tmux moved it, as with `detach-on-destroy off`) marks the session
  unknown and re-picks: back to a developer session if it landed on an
  internal one, or stays if it is a developer one. No timer.
- `list-sessions` failing with tmux's "no server running on" or "error
  connecting to ... (No such file or directory | Connection refused)" (3.2a
  and 3.7c) is no server; any other failure (a permission or protocol
  error) is an `attach_failed` (`list`) retried on the backoff. Only a
  server with no session at all, or no server, is absent.
- An attach that ends (`%exit`: its session closed; the stream ended: the
  server died; a reply for no command: `desync`) fails every waiting
  command, and `Run` tries again with backoff: the shape of the connection
  loop's (after n failures, uniform from 1 to 2^n s, capped at 60 s), reset
  after an attach that came up (defaults row "tmux control client"). A
  `Wake` that came while it was up is dropped then, so it never cuts that
  backoff short. The inventory observer debounces Wake (at most
  one per server a second): each costs one `list-sessions` while absent. When no server answers it waits in `StateAbsent` for `Wake`,
  with no timer (row-7 ruling fork 4). The backup `list-clients` poll of
  02h is 7f's. A `Program` that cannot run at all (tmux uninstalled) is an
  `attach_failed` try like any other: retried on the backoff, one
  `tmux.exit` a try, at most one a minute once the backoff is at its cap.
  Stopping `Run` logs `tmux.exit{reason="cancelled"}` when it was up.
- No method takes a command string. `Send` refuses the zero `typing.Line`
  (`typing.ErrZeroLine`) and a line whose server or server start id is not
  the attached one (`ErrWrongServer`) before anything is written; the check
  and the choice of the attached client are one hold of the lock.
- One command write is bounded (10 s): a tmux that stops reading its input
  ends the attach (`write_stalled`) and every waiting caller gets
  `ErrNotAttached`, instead of all of them waiting forever. A caller whose
  context ends while its write is blocked ends the attach too
  (`write_canceled`): a part-written command would put the stream out of
  step.
- `%pause` (as a notification or inside any reply) marks a pane paused;
  only the reply to the client's own `Continue(pane)` with `%continue %N`
  for that pane resumes it. The client never resumes a pane by itself:
  `%output` feeds nothing (row-7 ruling fork 5(a)).
- `ListPanes` reads eight fields a pane (ids, `pane_dead`,
  `pane_dead_status`, `pane_dead_signal`, size). A dead pane kept by
  `remain-on-exit` has `DeadStatus` when its program exited and
  `DeadSignal` when a signal killed it, never both: one of `hup`, `int`,
  `quit`, `abort`, `kill`, `segv`, `pipe`, `term`, else `other:<n>` (1 to
  127), else empty. tmux 3.7c prints the signal by name or by number: the
  name on macOS and the BSDs (`kill`), the number on Linux (`9`); 3.2a has
  no `pane_dead_signal` and never reports one. The signal is a diagnostic,
  not structure, so it fails open: text the client cannot read (a name
  this OS lacks, or a number outside 1 to 127) is `other`
  with no number, and the pane still comes back; tmux's text is never
  passed on. `<n>` is the host's own number (10 is USR1 on Linux, BUS on
  macOS), so the hub cannot compare it across hosts. A bad id, `pane_dead`,
  size or field count, or a status and a signal both present (tmux fills
  one or the other), is `ErrBadReply`. In 3.7c a pane is dead only once
  tmux has its status. On 3.2a, "dead, no status, no signal" is either the
  moment before the status is in or a pane a signal killed: it stays that
  way, because 3.2a never names the signal.
- `ControlDown(clients...)` is `AgentHealth.tmux_control_down`.

### Concurrency

```text
Run (one goroutine, owns the attach loop and the backoff timer)
  attach: proc tmux -V, proc list-sessions, start `tmux -C`  ->  conn
     |                                                        |
     |  go conn.read  (reader goroutine: owns Parser + stdout)  |
     |    Reply flags&1 -> pop pending[0] under pmu, honour     |
     |       ControlNotes (pause any; continue own only), send  |
     |       on its buffered chan                               |
     |    other messages -> paused set (pmu) -> Notify (no lock)|
     |    end of stream -> pmu: closed, reason; fail pending    |
     v
  typed methods (any goroutine): Client.mu -> conn; release;
     conn.do: wmu { pmu { append pending }; write line (bounded: a
       stall or ctx end kills the process, which ends the write) };
       wait chan or ctx
  Send: one hold of Client.mu checks server + start id and picks conn
  reader, on %sessions-changed: a token into conn.repick (chan, buffered 1;
    a full chan drops it: a burst coalesces); on %session-changed:
    Client.mu { session = $N }
  go repickLoop (started by attach after the handshake, ends with the
    conn): drains conn.repick; while internal: conn.do list-sessions, then
    conn.do switch-client -E; Client.mu only to read/write session and
    internal, never across conn.do
Locks: Client.mu (state, conn, start id, subs) is never held with wmu or
pmu. wmu then pmu, never the other way; nothing is called out under a lock.
Subscribe registers and reads conn in one hold of Client.mu; attach sets
conn and copies the list in one hold, so a subscription is sent once or
re-sent, never lost.
```

## Telemetry

| Signal | What it says |
|---|---|
| `fleet_agent_tmux_control_up{server_kind}` | 1 while attached, else 0 |
| `tmux.attached` (`server_kind`, `version`, `flags`, `session` = `developer` or `internal`) | an attach came up |
| `tmux.session_switched` (`server_kind`, `from`, `to`: ids) | the client moved from an internal session to a developer one |
| `tmux.repick_failed` (`server_kind`, `err_kind` = `list` or `switch`) | a re-pick failed; at most once a minute; the client stays up |
| `tmux.exit` (`server_kind`, `reason`, `err_kind` for `attach_failed`) | an attach ended or failed: `exit`, `eof`, `desync`, `write_stalled`, `write_canceled`, `attach_failed` (`version`, `list`, `start`, `handshake`, `subscribe`), `too_old`, `absent` (once per absence), `cancelled` |
| `tmux.subscription_confirmed` (`server_kind`, `name`) | the first value of a registered subscription on an attach |
| `fleet_agent_tmux_inventory_fields_dropped_total{field, reason}` | text fields an inventory read left blank: `field` is one of the seven text fields, `reason` is `over_bound` (over 1 KiB) or `mismatch` (the bytes are not the length tmux gave). Counted on every read, per pane record kept; needs `ClientOptions.Parser.Metrics` (the inventory observer passes it) |
| `tmux.inventory_field_dropped` (`field`, `reason`, `bytes`: the length tmux gave, `suppressed`) | the same, at most once a minute per field; never the value |

Never pane text, a session or window name, the `%exit` text or tmux's
stderr (05b).

Subscriptions per server, for 7f's cost run (proof 9, measured with all
three live): E13's `session_activity` (7f), E4's `window_activity` (7e) and
the inventory's `fleet-structure` (7d, coordinator decision amending row-7
ruling fork 5(f)), each checked by tmux about once a second.

Tests: `CGO_ENABLED=0 go test ./internal/agent/tmux/`. The client tests run
against real tmux 3.2 and the latest release in throwaway servers
(`tmuxtest`; set `FLEET_TMUX_32` and `FLEET_TMUX_LATEST`, or the tmux on PATH
stands in for the latest; a missing one skips, or fails with
`FLEET_REQUIRE_TMUX=1` as CI sets), and against a fake tmux script for the
version gate and the pause and continue rule. `testdata/flood-*.txt` are the
first lines of a flood during a command (`TestFloodDuringACommand` with
`FLEET_TMUX_RECORD=<dir>`). The recorded
transcripts and how they were made are in [testdata](testdata/README.md).
`go test` runs the fuzz seeds (the transcripts and `testdata/fuzz/FuzzParse/`);
to fuzz: `go test -run='^$' -fuzz='^FuzzParse$' -fuzztime=60s ./internal/agent/tmux/`
(`FuzzOutputRoundTrip` the same way); `make fuzz` runs both.
