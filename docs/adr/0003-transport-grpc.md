# 0003. Transport and contract: gRPC with protobuf

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

Agents and the hub exchange a continuous flow of state (tmux panes, goal-file
lines, usage readings, heartbeats) and commands (start a goal, send keys,
create a session, stop). The browser UI and the `fleet` CLI need the same
objects. The project may be open-sourced, so the contract has to be explicit,
versioned and easy for others to implement against.

## Options

1. gRPC with protobuf.
2. WebSocket with JSON messages.
3. Plain HTTP/JSON (REST) plus long-polling.

## Decision

gRPC with protobuf. The schema lives in `api/fleet/v1/*.proto`, package
`fleet.v1`, and is the single source of truth for hub, agent, CLI and UI.

- **Agent ↔ hub:** one long-lived bidirectional stream per agent over HTTP/2
  with TLS. Details (direction, identity, delivery semantics) are in
  [0004](0004-agent-connectivity.md).
- **UI and CLI ↔ hub:** the same `fleet.v1` services exposed in a
  browser-friendly form. Proposed: serve them with a library that speaks gRPC,
  gRPC-Web and a JSON-over-HTTP protocol from the same handlers, so the browser
  and `curl` can use the contract without a proxy. Live updates to the browser
  use server streaming or SSE. Confirm the library in a spike.
- **Tooling:** lint and breaking-change checks on the `.proto` files in CI;
  generated Go and TypeScript code is not hand-edited.
- **Versioning:** additive changes only within `v1` (new fields and messages,
  never renumbered or repurposed). Each side announces its version and
  capabilities on connect; the hub supports agents at least one minor version
  behind, which allows rolling upgrades. Breaking changes mean `fleet.v2`.

## Consequences

- Typed, generated clients in Go and TypeScript; contract drift shows up at
  compile time.
- Harder to inspect on the wire than JSON; mitigated by a `fleet debug stream`
  command that prints decoded messages, and by the JSON protocol for UI/CLI
  calls.

## Open questions

- Library choice for the browser-facing protocol (spike).
