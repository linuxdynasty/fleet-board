---
name: fleet-frontend
description: "Use when writing or changing the web UI under web/ or the mockup under mockup/: React with strict TypeScript and Vite, the generated Connect client, Watch with resume tokens, xterm.js attach flow control, plain language, and phone width."
---

# Frontend

Authority: [ADR 0007](../../../docs/adr/0007-web-ui-react.md),
[ADR 0016](../../../docs/adr/0016-browser-transport.md),
[ADR 0018](../../../docs/adr/0018-hub-api-and-contract-amendments.md),
[02b-hub-api.md](../../../docs/specs/02b-hub-api.md),
[ui-contract-map.md](../../../docs/specs/ui-contract-map.md), and the clickable
reference in [`mockup/`](../../../mockup/README.md).

## Stack and boundaries

- React + TypeScript with `strict: true` + Vite. Built to `web/dist`, embedded
  in `fleetd` with `go:embed`; Node is a build-time dependency only.
- The API client is **generated from `fleet.hub.v1`** (Connect). No
  hand-written request code and no import of `fleet.agent.v1` types: the UI
  never sees the agent wire. A missing field is a contract change first
  (`fleet-contract-rigor`), then regenerate.
- Types come from generated code; no `any`, no unchecked casts of API data, no
  `// @ts-ignore` without a linked reason.

## Live data

- One `FleetService.Watch` server stream per tab or phone. The subscription and
  `resume_token` are in the request; to change topics, reopen with the last
  token.
- Dedupe changes by `(id, version)`. On `RESYNC_REQUIRED`, drop local state for
  the topic and reopen with `send_initial` and no token. Reconnect with
  backoff and jitter; show a "reconnecting" state rather than stale data
  presented as live.
- `PANE_SUMMARIES` are latest-wins; do not queue them. Never-dropped topics are
  never silently skipped.
- Mutations send a `request_id` (UUID or ULID) and reuse it on a network retry;
  a second deliberate click gets a new key. Send `expected_version` where the
  API requires it and handle `VERSION_MISMATCH` by re-reading, not overwriting.

## Terminal attach (xterm.js)

- One WebSocket per attach carrying `AttachTerminalRequest` and
  `AttachTerminalResponse` (hub `terminal.proto`; the first frame is `AttachOpen`
  with the single-use ticket, 02f); never the agent wire's `TermUp`/`TermDown`.
  **Ack bytes to the hub only after xterm.js has written them** (`AttachAck`);
  never ack on receipt. Never buffer unboundedly in the browser: the hub stops reading the
  agent past the high-water mark and the PTY blocks, which is the intent.
- Viewer attaches are read-only: do not send input. Attaching to a goal window
  shows a banner because keystrokes reach a running harness.
- Test with a producer faster than xterm.js can write.

## Plain language, details behind a toggle

Say what is happening in plain words ("Claude is waiting for your answer", not
`PromptDetected`). Raw contract names, typed commands, hashes and ADR
references live behind one **details** toggle, as in `dt()` in
`mockup/fleet-core.js`. Prompts show the exact choice that will be sent;
destructive choices default to Deny and nothing is sent on timeout. Untrusted
text (screens, goal lines, issue titles) renders as text, never as HTML.

## Layout and accessibility

The layout works down to phone width (375 px): no horizontal page scroll, the
summary strip wraps, touch targets are usable, and the approval flow works on a
phone. Keyboard model follows the mockup (`i` details, jump switcher). Meet
basic accessibility: labels, focus order, contrast in light and dark.

## Tests and gates

```bash
npm run typecheck && npm run lint && npm test     # in web/
npm run build
```

Component tests for each view state (loading, empty, offline, resync,
error), plus an end-to-end browser test for any flow that sends a command. Keep
the bundle small; this is an internal tool, but watch it. Take a phone-width and
desktop screenshot for a visible change and cite what you saw. When the mockup
and the product diverge, update `ui-contract-map.md` in the same PR.
