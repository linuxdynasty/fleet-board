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
// capability f1 v1, RepeatSafe). The wiring row registers it.
```

## Order of checks

1. **Shape**: the name matches `[a-z0-9][a-z0-9-]{0,31}`, the command names
   this server's kind, and no harness, resume or lone profile is set
   (`INVALID_NAME`, `INVALID_ARGUMENT`, `HARNESS_NOT_ALLOWED`).
2. **Drain**: a harness start while draining is `DRAINING`; a login shell
   runs on (01f M4).
3. **Workdir**: resolves to a checkout under an allowed repo root, else
   `CHECKOUT_MISSING` or `REPO_ROOT_NOT_ALLOWED`. No roots configured
   refuses everything.
4. **tmux**: `has-session` decides create vs reuse; a missing server reads
   `TMUX_SERVER_DOWN`. A lost create race (`duplicate session`) takes the
   exists path.

## Tagging

A new session starts with the `FLEET_*` environment (`-e`, before the first
process forks) and the `@fleet_*` session options, so the observer
attributes it whatever it reads first. The pane key is
`server/server_start_id/pane_id`, with the start id derived as the
control-mode handshake derives it. Tag failures are not fatal: the session
exists and answers, and only its attribution degrades.
