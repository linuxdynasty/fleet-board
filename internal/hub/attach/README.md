# internal/hub/attach

The hub side of terminal attach (J1): single-use attach tickets and the
bridge that pairs one `AttachTerminal` client with one agent `Terminal`
stream by `attach_id`.

- `tickets.go`: `Store` mints 256-bit tickets bound to principal,
  session, read-only mode and Origin, valid 60 s. A burn deletes the
  ticket whether or not the attach then succeeds; a repeated
  `(principal, request_id)` never re-mints (same body is
  already-delivered, another body a conflict).
- `bridge.go`: `Registry` holds one fleetd process's tickets and bridges,
  shared by the FleetService handlers and the agent gateway. A `Bridge`
  moves pending (OpenAttach sent) to live (agent TermReady) to closed,
  queues agent output and client TermDowns, and counts unacked output:
  past 256 KiB the gateway stops reading the agent, so HTTP/2 flow
  control blocks the agent's PTY read while Connect commands keep
  flowing. Close is terminal; `CloseMachine` ends every bridge on a
  machine whose agent is gone (AGENT_OFFLINE).

Flow, limits and close codes are in
[02f](../../../docs/specs/02f-hub-authentication.md); the W8 flow in
[03d](../../../docs/specs/03d-triage-sessions-logs.md).

Tests: `CGO_ENABLED=0 go test ./internal/hub/attach/`.
