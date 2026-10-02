# 0016. Browser transport: Connect, and WebSocket for terminal attach

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty (drafted with Claude Opus 5.5)
- Resolves: the open library question in [0003](0003-transport-grpc.md) and
  "server stream or SSE" in [0007](0007-web-ui-react.md)

## Context

The browser UI and the CLI use the `fleet.hub.v1` contract ([0017](0017-contract-packages.md)). The UI needs unary
calls, live updates (≈ 140 KB/s across 10 browsers at 50 machines, latest-wins
for summaries) and interactive terminal attach (both directions, never
dropped). Browsers cannot stream a request body, so no fetch-based protocol
gives bidirectional streaming.

## Options

1. Connect for everything; SSE or WebSocket only where Connect cannot work.
2. gRPC-Web through a proxy.
3. Hand-written JSON over WebSocket for everything.

## Decision

Option 1 ([04](../specs/04-technology-review.md#q5-browser-fan-out-and-terminal-attach)).

- Every UI, CLI and phone call is a `fleet.hub.v1` Connect handler (connect-go), which also answers
  gRPC and gRPC-Web from the same code. The TypeScript client is generated.
- Live updates: one Connect server stream per tab or phone
  (`FleetService.Watch`); the subscription and a `resume_token` are in the
  request, so changing topics or reconnecting reopens the stream and resumes;
  `RESYNC_REQUIRED` tells the client to re-read. Slow consumers lose
  summaries only.
- Terminal attach: a WebSocket per attach carrying `TermUp`/`TermDown`. The
  browser acks bytes after xterm.js has written them; the hub stops reading the
  agent's `Terminal` stream past a high-water mark, so HTTP/2 flow control
  blocks the agent's PTY read. Nothing is dropped.
- The UI is built by Vite, embedded with `go:embed` and served by
  `http.FileServerFS`; Node is a build-time dependency only.

## Consequences

- One typed contract for UI, CLI and agents; no proxy.
- One long-lived stream per tab keeps plain-HTTP localhost under the browser's
  6-connection limit.
- Terminal flow control is application code on both ends (ack messages); it is
  tested with a producer faster than xterm.js.
