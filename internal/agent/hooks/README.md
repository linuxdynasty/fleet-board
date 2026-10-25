# internal/agent/hooks

Agent-side goal hooks (G1): install, verify and uninstall the user-level
hooks from one shared definition. See [01d](../../../docs/specs/01d-goal-hooks.md)
(the contract), [01d3](../../../docs/specs/01d3-cursor-goal-hooks.md) (Cursor)
and [01d4](../../../docs/specs/01d4-muse-sessionstart.md) (Muse timing).

## Seams

- `Ensure(home, dataDir, harness, workdir, expectedVersion)` installs the
  script, merges the user config (or refreshes the Muse plugin bundle),
  verifies the scripts with synthetic payloads, and returns the
  `GoalHooksState`. A repo-owned checkout reports `repo_ships_own=true`
  and its workdir is left untouched.
- `Uninstall(home, dataDir, harness)` removes only fleet entries, keeps
  the backup, and deletes the scripts.
- `RepoShipsOwn(workdir)`, `MergeUserConfig`, `RemoveUserConfig`,
  `RenderFresh`, `Command`, `IsFleetCommand`, `Trust`, `ReasonOf`.

## How the pieces fit

The settings entries only point at the script; the command string is
fixed and the script carries its version (`HOOK_VERSION`). The merge
reads the user file, backs it up to `<file>.fleet-backup.<timestamp>`,
and rewrites it atomically: only entries whose command points into the
fleet hooks directory are added or updated, everything else keeps its
bytes and its order. An unparseable user file fails with
`HOOKS_INSTALL_FAILED` and is left untouched.

The hook script (`hook.py`, one copy per harness directory) matches a
payload against the goal registry: session id first, then the pane
(`TMUX_PANE`, which reaches Claude, Codex and Cursor hooks), then the
working directory (Muse `cwd`, Cursor `workspace_roots`) only until the
session id is learned and only when exactly one pane is registered for
that checkout. Anything else exits 0 silently. Matched mapping events
learn the session id and spool it; turn-end events spool and run the
Stop guard from the goal file (`DONE` and live-`WATCH` or
`OWNER`+`ACTION` allow, the rest blocks; a stop whose status is not
completed is never held). `SessionStart` stays inert everywhere.
A repo-owned entry stays fully silent.

## Registry and spool (seam for the registry lane)

`FLEET_GOAL_REGISTRY`, else `$FLEET_AGENT_DATA_DIR/goal-registry.json`,
else `~/.local/share/fleet-agent/goal-registry.json`:

`{"version": 1, "entries": [{"goal_id", "harness", "pane",
"worktree_path", "harness_session_id", "repo_driver", "repo_ships_own"}]}`

Events spool as one JSON line each to `FLEET_HOOK_SPOOL`, else
`<data-dir>/hook-events.jsonl`: `session_mapping`, `turn_end`
(`allowed`, plus `status` when the payload names one), `interrupt`.
Muse plugin hooks run with a cleared environment, so they use the
`HOME`-based defaults. The harness feeds the payload on stdin;
`--input-file` reads it from a file instead, for verify runs (which
start the script through the one-shot helper, without stdin).

## Judgement calls

- Codex `Stop` is notify-only: no probe shows Codex accepting a Stop
  block, so the hook never guesses an output shape. Codex additionally
  wires `UserPromptSubmit` and `PreToolUse` because it fires no prompt
  hook for `/goal` lines (02h names them as the session-id fallback).
- Trust: Claude Code and Cursor never report `PENDING`; Codex and Muse
  do, with the approve command named, and the agent never approves.
- Every parse failure fails open (exit 0): a hook must never break a
  turn. Goal-file reads cap length and strip control characters.

## Tests

Table tests over per-harness fixtures in `testdata/` (payload shapes
follow the spec probes: Claude 2.1.286, Codex 0.160.0, Muse 1.4.x,
cursor-agent 2026.10.01). Home and the data dir are always temp dirs.
`hook.py` must stay python3.9-compatible (macOS system python3).
