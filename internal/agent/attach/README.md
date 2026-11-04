# internal/agent/attach

The agent side of terminal attach (J1, [01f](../../../docs/specs/01f-agent-control-goals-and-policy.md#j-terminal-attach)):
one grouped view session and PTY client per browser attach, bridged to the
hub's `Terminal` stream by the Manager, driven by the OpenAttach and
CloseAttach command handlers.

- `manager.go`: `Manager` opens (view, client, land wait, dial, ready, arm)
  and closes (hangup, kill view, DETACHED) attaches; `finish` is terminal
  and idempotent, so every pump does its own cleanup.
- `handler.go`: the `OpenAttach`/`CloseAttach` command handlers.
- `pumps.go`: `outPump` renders the PTY to the hub (stripped, never
  dropped); `inPump` carries input, resize, selects and close into the PTY.
- `pty.go`: `Client`, one PTY-backed `tmux attach` child; `Close` hangs up
  (HUP, then KILL past the grace) and reaps, safe to call twice.
- `session.go`: view sessions (`fleet-view-<attach>`) on the observer's
  servers; `strip.go` removes viewer-acting escape sequences.

The Terminal stream hangs off the Connected session (`internal/agent/conn`):
it dies with the session and never outlives it.

Tests: `CGO_ENABLED=0 go test ./internal/agent/attach/`.
