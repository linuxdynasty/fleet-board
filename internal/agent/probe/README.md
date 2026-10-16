# internal/agent/probe

The agent side of usage probes (v1 PR 8, issue #36, D1 slice): `EnsureProbe`
and `StopProbe` handlers plus the `Manager` that owns probe passes.

## What one pass does

1. Finds the harness binary on PATH (else `HARNESS_NOT_INSTALLED`).
2. Brings up `probe-<hash>` on the `fleet-probe` server in the probe
   folder, respawning a dead session (3 tries).
3. Captures until the screen settles, then classifies it (`classify.go`).
4. Trust screen: the result reports `FOLDER_UNTRUSTED` (01 C3) while
   health reports `STARTUP_PROMPT_SEEN` with kind `trust` (03b, the
   pacing block); type nothing. Update or release-notes screen:
   `STARTUP_PROMPT_SEEN` with the kind, type nothing. Anything else
   unproven: type nothing (`PANE_BUSY`, `READY_TIMEOUT`,
   `PROBE_START_FAILED`).
5. Idle prompt with an empty input line: types only the status command
   (`/usage`, `/status` on Codex), captures the answer, parses it with
   the harness's own `ParseUsage`, and emits `ProbeHealth` with a
   `UsageReading`. No usage screen means `ProbeHealth` only, never a
   guessed reading.

Login-screen markers are deliberately absent: no fixture shows one, so a
login screen reads as unknown and types nothing. They land with
live-probe fixtures, never invented.

## Seams (tests observe here)

- `Classify` / `StatusCommand`: pure, table-tested over the harness
  teams' own fixtures under `internal/harness/*/testdata`.
- `Manager` + handlers: driven through `exec.Request` with a fake
  `Backend` (scripted screens, every sent key logged) and a fake
  emitter. The fake fails on overlapping backend calls, proving one
  owner under `-race`.
- `TMux`: against a real `tmuxtest` throwaway server (tmux 3.8 here;
  CI adds 3.2a).

## Passive signals (D3)

Observed panes also contribute usage without being asked (01 D3, 03b).
`passive.go` holds that half: `BuildPassiveReading` turns a banner the
agent already sees (Claude only, the one spec-recorded shape) into a
`USAGE_SOURCE_PASSIVE` reading with an empty probe id, and
`PassiveCache` emits it only when a bucket's percent or reset differs
from the last emission for that account, so a repainted status line
costs nothing. A failed parse never emits. Other harnesses refuse with
no parser until their banner shapes are recorded.

## Follow-ups (not this slice)

- The observer call into `BuildPassiveReading`/`PassiveCache` on
  observed panes (this slice is parse, build and dedup only). That
  wiring also owns wrap reassembly: a banner split across a narrow
  pane's rows must be joined at capture (`capture-pane -J`), never by
  the parser joining lines blindly.
- Replacing the spec-recorded banner fixtures with a live near-limit
  capture when one lands, and only then claiming passive in the
  conformance table.
- Wiring into `cmd/fleet-agent` (manager construction, PATH lookup via
  a `proc.LookPath` helper, starting `Run`).
- `Snapshot.probes[]` from manager state, account-mismatch refusal
  (needs identity reads), harness profiles, the attached-client skip,
  and the `fleet_agent_probe_*` telemetry with its docs mapping.
