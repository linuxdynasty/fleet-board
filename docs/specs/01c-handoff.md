# 01c. Hand-off from capabilities to contracts

- Status: Draft for review
- Date: 2026-10-01
- Part of: [01-agent-capabilities.md](01-agent-capabilities.md)

## Contract-readiness rules

- **Identifiers:** `machine_id`, `account_id` (hub) vs `account_key` (agent),
  `session_id` (hub-assigned), `goal_id`, `prompt_id`, `probe_id`, `command_id`,
  pane key `(server, server_start_id, pane_id)`, normalized repo key.
- **Time:** UTC `Timestamp` everywhere, plus the machine timezone in B1.
- **Numbers:** percentages as integers 0–100 with an explicit `unknown` case,
  never 0 for unknown; sizes in bytes.
- **Ordering:** one monotonic `seq` per agent that survives reconnects (the
  offline ring replays in order); a snapshot supersedes earlier events.
- **Idempotency:** `command_id` on every command; composite steps as
  `command_id+step`; F3 explicitly not idempotent.
- **Errors:** one status enum (`OK`, `FAILED`, `EXPIRED`, `REJECTED_BY_POLICY`,
  `UNSUPPORTED`, `UNKNOWN_OUTCOME`, `AWAITING_LOCAL_CONFIRM`) plus a structured
  reason code shared with preflight.
- **Versioning:** capability strings with versions; parser version and
  supported harness versions (C1); hook version (G1); policy schema version
  (B1); settings generation (A8); a maximum message size for captures.

## Changes needed in accepted ADRs

To be made in step 2 as a superseding ADR, not by editing the originals:

- 0004 and 0006: the developer's normal tmux server is the default for listing,
  attaching, creating sessions and running goals (`default_server` defaults to
  `control`; local policy can lower it); the separate `tmux -L fleet` server
  becomes optional; browser attach moves to v2.
- 0004: add hub settings delivery (A8), agent `Bye` (A7), `SnapshotRequest`
  handling, `StartGoal` and `StopGoal` as agent-side commands (G3, G4),
  `EnsureProbe`/`StopProbe` (D1), `Undrain` (M4), `prompt_id` on `SendKeys`,
  `RunHarnessCommand` with the command classes (F8);
  remove `RunExec` from v1; move Tailscale lookup to v3.
- 0009: labels are assigned on the hub; local config can only add.
- 0010: harness transcripts ship incrementally (K3).

## Volumes (estimates, to be measured)

Assumes 50 machines, 10 panes each, 3 accounts.

| Stream | Rate | Size |
|---|---|---|
| Heartbeats | ≈ 3.3/s | < 1 KB |
| Activity transitions | bursty, ≈ up to 10/s | < 0.5 KB |
| Pane summaries | ≤ 100/s, droppable | 1–4 KB |
| Resource samples | ≈ 1.7/s | < 1 KB |
| Goal state | a few per goal per hour | < 2 KB |
| Usage readings | 3 accounts × every 10–15 min | 1–3 KB |
| Terminal logs | unknown | estimate 1–20 MB/hour per busy pane raw; **measure before step 3** |
| Snapshots | on connect | 10–100 KB |

## Testing approach

- Interfaces with fakes for tmux, harness adapters, power, network, clock and
  file watching.
- tmux capabilities against real tmux 3.2 and the latest release in CI.
- Harness parsers against captured screens (the real `/status` and `/usage`
  captures from 2026-09-28/29 are the first fixtures), one set per harness
  version. Versions move fast: Codex went 0.157.1 → 0.159.2 and Muse 1.4.0 →
  1.4.1 between 2026-09-28 and 2026-09-30, so fixture capture is a scripted
  job (`fleet-agent capture-fixtures`), not a manual step. Fixtures are
  scrubbed of emails, names and installed skill names before they are
  committed.
- Agent end to end against an in-memory fake hub; hub against fake agents.
