# internal/agent/control

The agent's pane-control commands (plan row 18, issue #67). This slice runs
F1 `EnsureSession` and F2 `EnsureWindow` for login shells: create the named
session on the bound server (or the named window in a session), or attach
to the one already there with `reuse`. A harness to start, or resume of a
harness conversation, is refused until the adapter start surface lands in a
later slice. F3 `SendKeys` types operator free text (`OPERATOR_TEXT` only)
into a harness pane at a proven-ready prompt; every other purpose waits for
its slice. F4 `Interrupt` sends a fixed C-c to the pane: no screen gate (a
turn is running, so a ready prompt must never be required), idempotent in
effect, so the class is `RepeatSafe` and a resume re-sends. F5
`RespawnPane` re-runs a dead pane's own command through a plain one-shot
`respawn-pane` (no `-k`, so tmux itself refuses a pane that is still
active); a live pane is refused `PANE_BUSY`, not retryable, and any
harness restart field waits for the adapter slice. F6 `KillWindow` kills
a window by its `@N` id: a stale start id or a window the listing does
not know reads `WINDOW_NOT_FOUND`, and the scheduler's fleet cell is
refused `ROLE_INSUFFICIENT` on a window the fleet's tags do not mark.
E11 `SetPaneAvailability` records the developer's board mark for one pane on
the observer's in-memory overlay (or clears it): no tmux call, no screen
gate, `RepeatSafe` like the other declarative kinds.

```go
runner, _ := proc.NewRunner(tel)
caller, _ := proc.For("tmux")
e, err := control.New(control.Options{
	Runner: runner, Caller: caller, Socket: sock, Server: "default",
	Kind: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
	Roots: func() []string { return pm.Policy().AllowedRepoRoots },
	ResolveWorkdir: resolveWorkdir, // the repo inventory owns this (row 9)
	MaxWindows: func() uint32 { return maxWindows }, // the merged settings value (03e2)
	Sender: client, // the observer client of internal/agent/tmux
	CheckInput: checkInput, // Observer.CheckInput with the server closed over
	Marks: observer, // the inventory observer holding the availability overlay
})
res := e.EnsureSession(ctx, cmd.GetEnsureSession())
res := e.EnsureWindow(ctx, cmd.GetEnsureWindow())
res := e.SendKeys(ctx, cmd.GetSendKeys())
res := e.SetAvailability(ctx, cmd.GetSetPaneAvailability())
// Handlers: h := e.Handler() satisfies exec.Handler (Kind EnsureSession)
// and w := e.WindowHandler() (Kind EnsureWindow), each with tmux.control
// plus the server-specific capability, RepeatSafe; s := e.SendKeysHandler()
// (Kind SendKeys) is NotRepeatSafe: a resume ends UNKNOWN_OUTCOME and never
// retypes; i := e.InterruptHandler() (Kind Interrupt) is RepeatSafe: a
// resume re-sends the C-c; r := e.RespawnHandler() (Kind RespawnPane) is
// RepeatSafe: a resume respawns again; a := e.AvailabilityHandler() (Kind
// SetPaneAvailability) is RepeatSafe: a resume records the mark again;
// k := e.KillWindowHandler() (Kind KillWindow) is RepeatSafe: a resume
// kills again, and answers the role-matrix variant (fleet_window only
// for a window the fleet's tags mark, developer_window else). The
// wiring row registers them.
```

## Order of checks

1. **Shape**: the session name matches `[a-z0-9][a-z0-9-]{0,31}` (F1), the
   window name is 1-256 bytes with no control characters (F2,
   `INVALID_NAME`, so the line-based window reads hold together), the
   session target names this server's kind by name, session id or snapshot
   tmux id (`$3`) plus server start id, and no harness, resume or lone
   profile is set (`INVALID_ARGUMENT`, `HARNESS_NOT_ALLOWED`).
2. **Drain**: a harness start while draining is `DRAINING`; a login shell
   runs on (01f M4).
3. **Workdir**: resolves to a checkout under an allowed repo root, else
   `CHECKOUT_MISSING` or `REPO_ROOT_NOT_ALLOWED`. A `..` element in the
   relative path is refused outright, never clamped. No roots configured
   refuses everything.
4. **tmux**: F1 `has-session` decides create vs reuse. A missing server
   reads `TMUX_SERVER_DOWN`, unless starting it is allowed: `new-session`
   then starts it implicitly (03d). A lost create race (`duplicate
   session`) takes the exists path. F2 never creates sessions: a missing
   one reads `SESSION_NOT_FOUND`. Past the refusal checks one call holds
   the server lock through list, find, cap and create, so two racing
   commands cannot both land under the cap or the same name. An existing
   window reuses, or stays a conflict unless its stored goal tag matches
   the retried goal; past the per-session window cap a create reads
   `MAX_WINDOWS_REACHED`. A window whose tagging fails is removed again,
   so the retry never meets an orphan it cannot own.
5. **Shape** (F3, no tmux touch): the purpose is `OPERATOR_TEXT` (an
   `UNSPECIFIED` purpose or an `answering_prompt` outside a prompt answer
   reads `INVALID_ARGUMENT`; `PROMPT_ANSWER`, `GOAL_NUDGE` and `GOAL_RESUME`
   read `UNSUPPORTED`, each landing in a later slice), at least one key, a
   pane on this server shaped `%N`. The keys' own shape (literals without
   control characters, named keys from the closed set) fails here too: it is
   the caller's malformation, never tmux's.
6. **Pane** (F3): the key's start id still matches the server's (a stale one
   reads `PANE_NOT_FOUND`), the pane is listed (else `PANE_NOT_FOUND`) and
   alive (a dead one reads `PANE_BUSY`, retryable).
7. **Input** (F3): the `CheckInput` read at that moment (02h E13) names a key
   within the last 10 minutes (the dashboard banner's window): `PANE_BUSY`,
   retryable, and nothing typed.
8. **Screen** (F3): the pane runs a known harness (a shell or unknown program
   reads `PANE_BUSY`, retryable: this slice types into harness panes only;
   OpenCode is deferred, not unknown: it takes no probe session and has no
   ready parser, so every `OPERATOR_TEXT` to one reads `PANE_BUSY`,
   retryable, until row 8 lands that parser) and its fresh styled capture
   (`capture-pane -p -e`: the Claude placeholder check needs the faint
   attribute a plain read strips) proves a ready prompt with an empty input
   line through the probe's screen classifier. A folder-trust screen reads
   `FOLDER_UNTRUSTED`, a startup screen `STARTUP_PROMPT_SEEN`, typed text in
   the line `PANE_BUSY` (retryable), anything else `READY_TIMEOUT`
   (retryable). One pane's check-and-send holds that pane's lock from the
   pane listing through the typing, so two commands to one pane cannot both
   pass the gate before either types; different panes never wait on each
   other. A Cursor pane that proves ready still stays `PANE_BUSY`: row 30
   owns the styled-input rule. A capture the reply bound cut short is
   refused `READY_TIMEOUT`: an incomplete screen proves nothing.
9. **Send** (F3): each key goes as its own `typing.Line` in order through the
   observer client, counted on success. A refused send ends `INTERNAL`
   naming how many typed; a send whose write reached tmux but whose reply
   never came back ends `UNKNOWN_OUTCOME` naming what is certain, and the
   hub asks a person whether the rest landed instead of sending it again
   (the class is `NotRepeatSafe`: a retry after `UNKNOWN_OUTCOME` is refused
   by the executor, never retyped). The OK result names the count and
   carries no screen text.
10. **Pane and send** (F4): the pane key names this server, a `%N` pane id
    and a server start id (`INVALID_ARGUMENT` else, before any tmux call),
    the start id still matches the server's and the pane is listed (a stale
    key or a missing pane reads `PANE_NOT_FOUND`) and alive (a dead pane
    reads `PANE_BUSY`, not retryable: nothing becomes interruptible by
    waiting; respawn is F5's). The check-and-send holds that pane's lock,
    shared with F3, so a C-c never lands between F3's screen read and its
    typing. The fixed C-c travels as a `typing.Line` through the observer
    client, so the server-start check is atomic with the write: a server
    that restarted under the checks refuses with `PANE_NOT_FOUND` instead
    of taking the C-c on its reused pane id. The line borrows the
    `OPERATOR_TEXT` purpose (F3 already sends C-c under it; a new purpose
    needs an arbiter ruling) and emits no `typing.sent`. A detached client
    reads `TMUX_SERVER_DOWN`, a send whose reply never came back reads
    `UNKNOWN_OUTCOME`, and a refused send re-reads the listing: a gone
    pane reads `PANE_NOT_FOUND`, a dead one `PANE_BUSY`, anything else
    `INTERNAL`. The OK result names the pane and carries no screen text.
11. **Pane and respawn** (F5): the pane key names this server, a `%N`
    pane id and a server start id (`INVALID_ARGUMENT` else, before any
    tmux call), and no harness, resume, profile or harness conversation
    is set (a restart reads `HARNESS_NOT_ALLOWED`, a lone profile or
    conversation `INVALID_ARGUMENT`: this slice re-runs the pane's own
    command only). The start id still matches the server's and the pane
    is listed (a stale key or a missing pane reads `PANE_NOT_FOUND`)
    and dead (a live pane reads `PANE_BUSY`, not retryable: respawn
    never kills live work). The check-and-respawn holds that pane's
    lock, shared with F3 and F4. The respawn is a one-shot
    `respawn-pane -t` with no `-k` flag and no command, so tmux re-runs
    what the pane ran and itself refuses a pane that came alive under
    the call. A refused respawn re-reads the listing: a gone pane reads
    `PANE_NOT_FOUND`, a live one `PANE_BUSY`, anything else `INTERNAL`.
    The OK result names the pane and carries no screen text.
12. **Window and kill** (F6): the window key names this server, an `@N`
    window id and a server start id (`INVALID_ARGUMENT` else, before any
    tmux call). One call holds the server lock from the generation check
    through the kill, shared with F2's create sequence. A stale start id
    or a window the server-wide listing does not know reads
    `WINDOW_NOT_FOUND`, never a kill at a reused id. The executor's
    fleet cell (the scheduler's 02g `F` row) is judged here, where tmux
    is visible: a window neither its own `@fleet_session_id` window
    option nor its session's marks reads `ROLE_INSUFFICIENT`
    (`REJECTED_BY_POLICY`), before any kill. A refused kill re-reads
    the listing: a gone window reads `WINDOW_NOT_FOUND`, anything else
    `INTERNAL`. The OK result names the window and carries no screen
    text.
13. **Mark** (E11, no tmux touch): the pane key names this server, a `%N`
    pane id and a server start id (`INVALID_ARGUMENT` else, before the
    overlay), and the reason is one the hub commands (`MARKED_ON_BOARD`,
    `CLEARED_ON_BOARD`, `GOAL_MERGED`; an empty reason or one the agent
    itself reports — output, idle threshold, goal started, pane changed —
    reads `INVALID_ARGUMENT`). The owner's fresh-context choice is kept
    only while the pane is marked available; a clear or a merged goal
    drops it. The overlay's refusal (unknown pane, stale start) reads
    `PANE_NOT_FOUND`, not retryable: the hub re-reads the Snapshot and
    re-sends. The OK result names the pane and carries no detail.

## Results report stored tags

The OK result reads the `@fleet_*` options back: ownership and ids come
from tmux, never from the request, so attaching to a developer session
reports it as the developer's. A retry that finds the tag it asked for
completes OK (F1 and F2 are idempotent); any other session or window under
the name with `reuse` unset stays a conflict.

## Tagging

A new session starts with the `FLEET_*` environment (`-e`, before the first
process forks) and the `@fleet_*` session options, so the observer
attributes it whatever it reads first. A new window carries the
`FLEET_SESSION_ID`, `FLEET_OWNER` and `FLEET_GOAL_ID` environment of its
session and request, plus the `@fleet_*` window options including the first
pane's key, and `automatic-rename` off for the fleet's own window only
(01f). The pane key is `server/server_start_id/pane_id`, with the start id
derived as the control-mode handshake derives it. Tag failures are not
fatal: the session or window exists and answers, and only its attribution
degrades.
