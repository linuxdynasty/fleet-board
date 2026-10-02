# 0011. Contract amendments to 0004, 0006, 0009 and 0010

- Status: Accepted
- Date: 2026-10-01
- Deciders: linuxdynasty (drafted with Claude Fable 5.1)
- Supersedes, in part: [0004](0004-agent-connectivity.md),
  [0006](0006-tmux-sessions.md), [0009](0009-machine-groups-and-layered-settings.md),
  [0010](0010-session-logs.md)

## Context

Step 1 of the design phase ([01-agent-capabilities.md](../specs/01-agent-capabilities.md))
and its hand-off ([01c-handoff.md](../specs/01c-handoff.md)) found that the
accepted ADRs no longer match the agreed product principle (fleet-board is a
pass-through over the developer's own tmux and harnesses) or the capability
map. Step 2 ([02-contracts.md](../specs/02-contracts.md), `api/fleet/v1`)
needed a few more messages than 0004's outline. ADRs are not edited after
acceptance, so this ADR records every amendment in one place.

## Options

1. Edit 0004, 0006, 0009 and 0010 in place.
2. One superseding ADR listing the amendments (this one).
3. One new ADR per affected ADR.

Option 2: the changes are small individually and all come from the same two
sources (the pass-through principle and the contract), so one record keeps
the reasoning together. Option 1 breaks the "never edited" rule; option 3
spreads one decision over four files.

## Decision

### 0004 and 0006: the developer's tmux is the fleet's tmux

- The developer's normal tmux server (the one `tmux ls` shows) is the
  default for listing, attaching, creating sessions and running goals.
  `default_server` in local policy defaults to **`control`**; local policy
  can lower it to `observe` or `none`. Hub settings can still only tighten.
- The separate `tmux -L fleet` server becomes **optional**
  (`TmuxServerKind.FLEET`, capability `tmux.fleet_server`). The hidden
  `tmux -L fleet-probe` server stays for usage probes only.
- Fleet-created sessions and windows are tagged with `FLEET_SESSION_ID`,
  `FLEET_OWNER`, `FLEET_PANE_KEY`, `FLEET_GOAL_ID` and named by the template
  `{harness} {repo}#{issue}` (configurable); `automatic-rename` is turned off
  for fleet windows only.
- Browser attach (`Terminal` stream, J1) moves from "later" to **v2**, through
  a grouped view session so the developer's current window never moves.
  The copyable `ssh -t … tmux attach` line stays as the v1 fallback.
- `KillWindow` on a window the developer created, and `PruneWorktree` with
  `allow_dirty`, require `ROLE_OWNER`.

### 0004: command model and connection

- **Accepted kinds** become: `EnsureProbe`, `StopProbe`, `EnsureSession`,
  `EnsureWindow`, `SendKeys`, `Interrupt`, `RespawnPane`, `KillWindow`,
  `CapturePane`, `RunHarnessCommand`, `SetSessionAvailability`, `Preflight`,
  `StartGoal`, `EnsureWorktree`, `PruneWorktree`,
  `EnsureGoalHooks`, `OpenAttach`, `CloseAttach`, `SetLogCapture`,
  `SetLogShipping`, `Drain`, `Undrain`. `ProbeUsage` is replaced by
  `EnsureProbe`/`StopProbe` (D1): the hub assigns one prober per account and
  fails over.
- **`RunExec` is removed from v1.** `Command` field 99 and the name
  `run_exec` are reserved; capability `exec.raw` is reserved and never
  advertised. The `exec.raw: true` policy switch in 0004 has no effect in v1.
- **`SendKeys` carries `answering_prompt` (`prompt_id`)** from
  `PromptDetected`; if the prompt left the screen the agent answers
  `PROMPT_GONE` instead of typing. A new `PromptGone` event retires prompts.
- **`RunHarnessCommand`** (F8) replaces free-typed slash commands: the
  command must be in the machine's catalog (C4), the catalog hash current,
  and its class (`observe`, `session`, `goal`, `review`, `destructive`,
  `deny`) allowed for the requester's role and local policy. `deny` is never
  sendable; unknown commands are `deny`.
- **`StartGoal`** is an agent-side composite under one `command_id`, with
  steps reported as `CommandProgress`. There is no `StopGoal`: pausing,
  resuming, editing and clearing a goal use the harness's own `/goal`
  subcommands through `RunHarnessCommand` (or `Interrupt` where a harness has
  no pause), so fleet-board never defines a goal protocol of its own.
- **Hub settings delivery (A8):** `Settings{generation, labels, values}` is
  a hub message; the agent answers `SettingsAck{generation, effective,
  capped_fields}`. This is not a config push: it can only tighten, and the
  agent reports what the local ceiling capped.
- **Agent `Bye{reason}`** (A7) on SIGTERM, upgrade or re-enroll so the hub
  marks the machine offline at once.
- **`SnapshotRequest{reason}`** may be sent at any time; the agent answers
  with a full `Snapshot`.
- **`ResultAck`** from the hub lets the agent drop results from its local
  log; unacked results are listed in `Snapshot.unacked_results` and re-sent.
- **Two sequences.** Events carry an agent-wide monotonic `seq` that is
  persisted and survives reconnects (the offline ring replays in order; a
  snapshot supersedes lower seqs). Commands carry a per-epoch `seq`.
- **Tailscale lookup** in hub discovery moves to v3; cached → mDNS →
  configured addresses remain.
- `HelloAck` carries `max_message_bytes` (1 MiB) and
  `hub_last_event_seq`.

### 0009: labels and settings

- **Labels are assigned on the hub** and delivered in `Settings.labels`.
  Local config can add labels but cannot remove a hub-assigned one, so a
  machine cannot leave a stricter group by editing a file.
- The settings table gains: window name template, idle threshold, attach
  idle timeout, `share_identity_email` (owner), spool cap, `block_on_battery`,
  default probe interval and per-account overrides, `merged_grace`. All are
  typed fields in `SettingValues`; the agent reports the layer that set each
  (`SettingSource`).

### 0010: session logs

- **Harness transcripts ship incrementally by byte offset** like terminal
  logs (`LogChunk.stream = HARNESS_TRANSCRIPT`), not at session end, because
  goals run for days and `--continue` sessions do not end cleanly. K3 is v3
  and opt-in per repo or machine.
- **Capture scope:** goal panes are always captured; the developer's own
  sessions only when turned on per session (`SetLogCapture`).
- Chunks are zstd-compressed by the agent (measured 115× on a real goal).
  `LogAck.pause` lets the hub pause shipping from the log stream itself;
  capture continues into the spool and gaps are marked.

## Consequences

- 0004's "accepted kinds" list, envelope sequence wording, `ProbeUsage`,
  `RunExec`, `default_server: none` default and Tailscale step are
  superseded by this ADR. The rest of 0004 (dial-out, mTLS, enrollment,
  rotation, policy ceiling, reliability rules) stands.
- 0006's server table is superseded: default server is the primary, `fleet`
  optional, `fleet-probe` unchanged. Its observation, lifecycle and trust
  sections stand.
- 0009's label ownership and settings table are extended; the two-layer
  ceiling model stands.
- 0010's shipping paragraph is extended; storage, reading and retention
  stand.
- The contract (`api/fleet/v1`) is the normative form of every kind and
  field named here; this ADR does not restate field numbers.

## Open questions

- Whether `exec.raw` should ever be defined in a later version, or removed
  from the reserved list and forgotten.
- Whether a `CancelCommand` is needed for in-flight composites (see
  02-contracts.md open questions).
