# 01c. Hand-off from capabilities to contracts

- Status: Accepted
- Date: 2026-10-01
- Part of: [01-agent-capabilities.md](01-agent-capabilities.md)

## Contract-readiness rules

- **Identifiers:** `machine_id`, `account_id` (hub) vs `account_key` (agent),
  `session_id` (hub-assigned), `goal_id`, `prompt_id`, `probe_id`, `command_id`,
  pane key `(server, server_start_id, pane_id)`, normalized repo key.
- **Time:** UTC `Timestamp` everywhere, plus the machine timezone in B1.
- **Numbers:** percentages as integers 0–100 with an explicit `unknown` case,
  never 0 for unknown; sizes in bytes.
- **Ordering:** one monotonic `seq` per agent that survives reconnects, crashes
  and hub restores (persisted high-water mark, floor from the hub; the offline
  ring replays in order); a snapshot supersedes earlier events (02i).
- **Idempotency:** `command_id` on every command; composite steps as
  `command_id+step`; F3 explicitly not idempotent.
- **Errors:** one status enum (`OK`, `FAILED`, `EXPIRED`, `REJECTED_BY_POLICY`,
  `UNSUPPORTED`, `UNKNOWN_OUTCOME`) plus a structured reason code shared with
  preflight; `AWAITING_LOCAL_CONFIRM` is a progress state, never a result.
- **Versioning:** capability strings with versions; parser version and
  supported harness versions (C1); hook version (G1); policy schema version
  (B1); settings generation (A8); a maximum message size for captures.

## Changes needed in accepted ADRs

To be made in step 2 as a superseding ADR, not by editing the originals:

- 0004 and 0006: the developer's normal tmux server is the default for listing,
  attaching, creating sessions and running goals (`default_server` defaults to
  `control`; local policy can lower it); the separate `tmux -L fleet` server
  becomes optional; browser attach is v1 (ADR 0025).
- 0004: add hub settings delivery (A8), agent `Bye` (A7), `SnapshotRequest`
  handling, `StartGoal` as an agent-side command (G3) and goal control through
  the harness's own `/goal` subcommands (G4),
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
| Terminal logs | measured, see below | **26.9 MB/hour raw, 0.23 MB/hour zstd-3** per busy pane |
| Snapshots | on connect | 10–100 KB |

## Measured: one real goal (2026-10-01)

Muse 1.4.2 working eshu-hq/eshu#7398 for 60 minutes on macOS, recorded with
`tmux pipe-pane`, sampled every minute. Only sizes were kept.

| Measure | Result |
|---|---|
| Raw terminal output | 26.9 MB in 60 min (26.9 MB/hour) |
| Per minute | median 288 KB, p90 974 KB, max 1,734 KB, **min 274 KB** |
| gzip | 581 KB (46×) |
| zstd level 3 | 234 KB (115×) |
| zstd level 19 | 68 KB (400×) |
| Muse's own transcript for the session | 13 MB after 60 min |

Findings:
- **Most of the raw volume is screen redraw.** Even quiet minutes produced
  about 274 KB (the TUI repaints its timer, spinner and status line), which is
  why it compresses so well.
- **The agent compresses before shipping** (zstd, streaming). At about 0.23 MB
  per busy pane per hour, 30 busy panes are about 7 MB/hour, roughly 5 GB a
  month before retention. Network cost is negligible.
- **Harness transcripts are larger than compressed terminal logs** (13 MB/hour
  raw for Muse), so K3 stays opt-in and compressed.
- **Real prompt observed:** after an hour the goal stopped at a Muse approval
  prompt for `git fetch https://x-access-token:$(gh auth token)@github.com/…`,
  which embeds a token in a URL. E6 must flag prompts that put credentials on a
  command line as destructive, defaulting to Deny.

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
