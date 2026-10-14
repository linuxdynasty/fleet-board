# internal/agent/control

The agent's pane-control commands (plan row 18, issue #67). This slice runs
F1 `EnsureSession` for login shells: create the named session on the bound
server, or attach to the one already there with `reuse`. A harness to start,
or resume of a harness conversation, is refused until the adapter start
surface lands in a later slice.

```go
runner, _ := proc.NewRunner(tel)
caller, _ := proc.For("tmux")
e, err := control.New(control.Options{
	Runner: runner, Caller: caller, Socket: sock, Server: "default",
	Kind: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
	Roots: func() []string { return pm.Policy().AllowedRepoRoots },
	ResolveWorkdir: resolveWorkdir, // the repo inventory owns this (row 9)
})
res := e.EnsureSession(ctx, cmd.GetEnsureSession())
// Handler: h := e.Handler() satisfies exec.Handler (Kind EnsureSession,
// tmux.control plus the server-specific capability, RepeatSafe). The
// wiring row registers it.
```

## Order of checks

1. **Shape**: the name matches `[a-z0-9][a-z0-9-]{0,31}`, the command names
   this server's kind, and no harness, resume or lone profile is set
   (`INVALID_NAME`, `INVALID_ARGUMENT`, `HARNESS_NOT_ALLOWED`).
2. **Drain**: a harness start while draining is `DRAINING`; a login shell
   runs on (01f M4).
3. **Workdir**: resolves to a checkout under an allowed repo root, else
   `CHECKOUT_MISSING` or `REPO_ROOT_NOT_ALLOWED`. A `..` element in the
   relative path is refused outright, never clamped. No roots configured
   refuses everything.
4. **tmux**: `has-session` decides create vs reuse. A missing server reads
   `TMUX_SERVER_DOWN`, unless starting it is allowed: `new-session` then
   starts it implicitly (03d). A lost create race (`duplicate session`)
   takes the exists path.

## Results report stored tags

The OK result reads the session's `@fleet_*` options back: ownership and
ids come from tmux, never from the request, so attaching to a developer
session reports it as the developer's. A retry that finds the session tag
it asked for completes OK (F1 is idempotent); any other session under the
name with `reuse` unset stays a conflict.

## Tagging

A new session starts with the `FLEET_*` environment (`-e`, before the first
process forks) and the `@fleet_*` session options, so the observer
attributes it whatever it reads first. The pane key is
`server/server_start_id/pane_id`, with the start id derived as the
control-mode handshake derives it. Tag failures are not fatal: the session
exists and answers, and only its attribution degrades.
