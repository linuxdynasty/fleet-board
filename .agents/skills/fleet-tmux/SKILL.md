---
name: fleet-tmux
description: "Use when writing or changing code in internal/agent/tmux or anything that drives tmux: control mode, send-keys, capture-pane, the fleet and probe servers, and the developer's own server rules."
---

# tmux control

Authority: [ADR 0006](../../../docs/adr/0006-tmux-sessions.md),
[ADR 0004](../../../docs/adr/0004-agent-connectivity.md#interactive-attach-and-observation),
[ADR 0011](../../../docs/adr/0011-contract-amendments.md), and
[01-agent-capabilities.md](../../../docs/specs/01-agent-capabilities.md)
sections E, F, J.

## Servers

| Server | Holds | Rule |
|---|---|---|
| `tmux -L fleet` | goal windows and sessions made from the hub | agent may create, drive, close after the merged grace period |
| `tmux -L fleet-probe` | one hidden idle session per account for usage probes | internal only |
| default server | the developer's own sessions | untouched unless that machine's local policy sets `default_server: observe` or `control`; default is `none`. Never close a session the owner created |

Tests and live probes use a throwaway `-L <unique-name>` server and kill it
afterwards. Minimum tmux is 3.2; CI covers 3.2 and the latest.

## Observation: control mode

- One `tmux -C` client per server, attached with the `ignore-size` flag so it
  never resizes the developer's windows, and `refresh-client -f pause-after=<s>`
  for flow control (`%pause`, `%continue`, `%extended-output`). Verify each flag
  against tmux 3.2 before relying on it.
- Parse the control-mode protocol line by line; decode `%output` octal escapes;
  never assume one notification per read. Reconnect on `%exit`, re-list
  sessions, and rebuild state from `list-panes -F` rather than from memory.
- Key panes by `(server, server_start_id, pane_id)` plus `FLEET_*` environment
  tags; tmux ids restart from zero with a new server. Identify the harness from
  `pane_current_command` and the adapter, never the window name.
- No per-pane polling loop. `capture-pane` is for snapshots and fingerprints.
- Pane summaries are ANSI-stripped, at most one per pane per 5 s, droppable.
  Activity transitions, prompts and pane exit are never dropped
  ([02-contracts.md](../../../docs/specs/02-contracts.md#delivery-semantics)).

## Control: what may be sent

- `SendKeys` takes **literal text plus a small set of named keys** (`Enter`,
  `C-c`, `Escape`, arrows) and **never a tmux command string**. Construct the
  `send-keys` invocation from typed parts; use `-l` for literal text so tmux
  does not interpret key names inside it.
  In control mode the command is a text line: quote every argument and reject
  newlines in literal text, or one request can smuggle a second tmux command.
- Never type into a pane unless all of these hold: the pane is in a state that
  accepts the input, the input line is empty (a goal is never typed into a
  non-empty line), an answer is bound to its `prompt_id`, and local policy and
  approval pass. `SendKeys` never answers prompts without `answering_prompt`.
- `SendKeys` is not idempotent: after an agent restart it ends
  `UNKNOWN_OUTCOME` and is never retried by the hub.
- Permission prompts that are destructive default to No; the agent never
  auto-answers them.
- New windows get `FLEET_SESSION_ID`, `FLEET_OWNER`, `FLEET_PANE_KEY`. A running
  harness cannot gain environment variables, so reuse is decided by the goal
  registry, not by the environment (ADR 0019).

## Attach

Browser attach is a PTY-backed `tmux -L fleet attach -t <target>` per attach,
bridged to a WebSocket, with HTTP/2 flow control on the `Terminal` stream:
output is never dropped, the PTY blocks. `viewer` attaches are read-only (the
hub drops input).

## Tests

Test against a real tmux in a throwaway server, with a fake harness script for
screens. Cover: pane exit with status, window rename, a burst of output beyond
one read, a `%pause`/`%continue` cycle, tmux killed under the client, and keys
containing tmux key names as literal text. Report the tmux version used.
