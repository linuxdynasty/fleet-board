# internal/agent/control

The agent's pane-control commands (plan row 18, issue #67). This slice runs
F1 `EnsureSession` and F2 `EnsureWindow` for login shells: create the named
session on the bound server (or the named window in a session), or attach
to the one already there with `reuse`. A harness to start, or resume of a
harness conversation, is refused until the adapter start surface lands in a
later slice.

```go
runner, _ := proc.NewRunner(tel)
caller, _ := proc.For("tmux")
e, err := control.New(control.Options{
	Runner: runner, Caller: caller, Socket: sock, Server: "default",
	Kind: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
	Roots: func() []string { return pm.Policy().AllowedRepoRoots },
	ResolveWorkdir: resolveWorkdir, // the repo inventory owns this (row 9)
	MaxWindows: func() uint32 { return maxWindows }, // the merged settings value (03e2)
})
res := e.EnsureSession(ctx, cmd.GetEnsureSession())
res := e.EnsureWindow(ctx, cmd.GetEnsureWindow())
// Handlers: h := e.Handler() satisfies exec.Handler (Kind EnsureSession)
// and w := e.WindowHandler() (Kind EnsureWindow), each with tmux.control
// plus the server-specific capability, RepeatSafe. The wiring row registers
// them.
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
