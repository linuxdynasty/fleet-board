# 1d. Goal hooks (G1)

How fleet-board installs and uses harness hooks for goals. Capability G1 in
[01-agent-capabilities.md](01-agent-capabilities.md); contract
`EnsureGoalHooks` / `GoalHooksState` in [02-contracts.md](02-contracts.md).

## What the hooks are for

The loop itself is each harness's native `/goal` ([01b](01b-harness-commands.md#goal-subcommands)):
Claude, Codex and Muse keep working toward the objective without any fleet hook.
Fleet's hooks do three narrower jobs:

1. **Session mapping (E9).** Record the harness session id against
   `FLEET_PANE_KEY`, so G2 and K3 can find goal files and transcripts.
2. **Turn-end events (E8).** Tell the agent a goal turn ended, with the
   harness's last message, so stuck and done detection does not depend on the
   screen alone.
3. **Stop guard.** Refuse a stop while the goal file says the goal is
   unfinished and nothing is watching it (the same rule as eshu's
   `goal-continue.sh`: `DONE` → allow; `BLOCKED … WATCH=<live pid>` → allow;
   `BLOCKED` without a live watcher → refuse and escalate).

They never type into the pane, never answer prompts and never resume a goal
the developer interrupted.

## Scope: user level, inert outside fleet goals

Decided 2026-10-02: hooks are installed at **user level** (the developer's own
harness config), not into each repo.

| Harness | File | Events used |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `SessionStart`, `UserPromptSubmit`, `Stop` |
| Codex | `~/.codex/hooks.json` | `SessionStart`, `Stop`, `Interrupt` (notify only) |
| Muse | a plugin installed at user scope: `muse plugins install <bundle> --scope user` (see [Muse](#muse-hooks-are-a-plugin-package)) | `SessionStart`, `Stop`, `Interrupt` (to confirm in the Stop probe) |

- Every fleet hook exits 0 with no output unless `FLEET_GOAL_ID` is set in its
  environment. `StartGoal` sets it in the goal window's pane environment
  ([03c](03c-goals.md)), so the developer's own sessions, including ones on the
  same machine, see no change.
- Pane environment reaches hook commands: in the probe the hooks wrote to
  `$HOOK_LOG`, which was set only in the tmux pane (Claude and Codex both).
- The hook scripts live under the agent's data directory
  (`~/.local/share/fleet-agent/hooks/<harness>/`); the settings entries only
  point at them. The command string stays fixed across upgrades and the script
  carries its version.
- **One hook definition for all three.** Claude's `settings.json` hooks,
  Codex's `hooks.json` and Muse's Claude-compatible plugin `hooks/hooks.json`
  share one JSON shape (`{"hooks": {"<Event>": [{"matcher", "hooks": [{"type":
  "command", "command"}]}]}}`). The agent generates the three files from one
  definition; only the location differs.
- Pass-through ([ADR 0011](../adr/0011-contract-amendments.md)): no root, no repo files, no new
  accounts. The only new steps for the developer are approving the hooks once:
  Codex in `/hooks`, Muse with `muse plugins approve` (below).

## Install, verify, uninstall

`EnsureGoalHooks{harness, workdir, expected_version}`:

1. **Repo ships its own goal hooks?** The agent reads the workdir's project
   config (`.claude/settings.json`, `.claude/settings.local.json`,
   `.codex/hooks.json`, `.muse/hooks.json`). A project `Stop` hook or a goal router on
   `UserPromptSubmit` means the repo owns goals (eshu): report
   `repo_ships_own=true`, and the fleet hooks treat that workdir as inert so
   two Stop guards never fight. Nothing in the repo is changed.
2. **Merge, don't replace.** Read the user file, back it up to
   `<file>.fleet-backup.<timestamp>`, add or update only entries whose command
   points into the fleet hooks directory, keep every other entry and its order,
   write atomically (temp file + rename). Unparseable file → stop with
   `HOOKS_INSTALL_FAILED` and leave it untouched.
3. **Verify.** Run each script with a synthetic payload and no
   `FLEET_GOAL_ID` (must print nothing, exit 0), then with one (must emit the
   expected event). Report `hook_version`, `hook_hash` (sha256 of scripts plus
   entries), `verified`.
4. **Codex trust.** Codex runs a new or changed hook only after the user
   approves it in `/hooks` (trust is recorded per hook hash). `/hooks` is on
   the deny list ([01b](01b-harness-commands.md)) and the agent never passes
   `--dangerously-bypass-hook-trust`, so `GoalHooksState.trust_pending=true`
   goes to Triage: "approve fleet hooks once in Codex `/hooks` on <machine>".
   Goals still run without trust; only E8 and the Stop guard are missing.
   **Muse** works the same way: an installed plugin's hooks run only after
   the user runs `muse plugins approve fleet-goal-hooks`, and trust is
   recorded per definition hash (`trusted_definition_hash`), so a changed hook
   prompts again. The agent never runs `approve`; the same `trust_pending`
   Triage item names the command.

`fleet-agent hooks uninstall [--harness]` removes only fleet entries (same
match rule), keeps the backup, and deletes the scripts. Upgrades of the agent
re-run step 3; a hash change is shown in the Machines view, never applied
silently to a running goal.

## Who writes the goal file

Probe result: Codex does not fire `UserPromptSubmit` for `/goal …` lines, so a
hook cannot be the writer there. The **agent writes the initial goal file**
for every harness, just before `type_goal`, from what it already knows
(`goal_id`, objective, consent). The model updates the first line (`DONE`,
`BLOCKED …`) as the objective text instructs; hooks only read it.

- eshu repos keep their own file (`.claude/active-goal.<session_id>`) and
  their own `CONSENT:` handling; the agent reads it (G2) and writes nothing.
- Generic file location: open question (below). It must be writable from
  inside the harness sandbox and must not show up as a repo change.

## Interrupts

| Harness | Hook sees the interrupt? | Agent learns it from |
|---|---|---|
| Claude Code 2.1.286 | no: `Stop` does not fire on Esc | screen: "Interrupted · What should Claude do instead?" |
| Codex 0.160.0 | `Interrupt` fires; output is ignored ("invalid interrupt hook JSON output" when it tried to block) | `Interrupt` hook event plus screen "Goal paused" |
| Muse 1.4.2 | an `Interrupt` hook event exists (binary, not yet probed live) | screen: "goal paused by interrupt" |

An interrupt is the developer's decision. The agent reports
`HarnessCondition` (E12) and the hub opens a Triage item "goal interrupted —
resume?"; resuming sends the harness's own command (`continue` for Claude,
`/goal resume` for Codex and Muse). Nothing resumes automatically.

## Probe record (2026-10-02)

Throwaway tmux server, logging hook on every event, `HOOK_LOG` set in the pane.

**Claude Code 2.1.286**, hooks in a project `.claude/settings.json`:

| Step | Events logged |
|---|---|
| `/goal create 40 files, one per turn` | `UserPromptSubmit` (payload `prompt` = the `/goal` line) |
| Esc mid-goal | none |
| `continue` | `UserPromptSubmit` |
| goal finished ("✔ Goal achieved") | `Stop` |

**Codex 0.160.0**, hooks in `.codex/hooks.json`, approved in `/hooks`. Event
list from the Codex hooks docs: `SessionStart`, `SessionEnd`, `PreToolUse`,
`PostToolUse`, `PermissionRequest`, `PreCompact`, `PostCompact`,
`UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`, `Interrupt`.

| Step | Events logged |
|---|---|
| `/goal create 40 files …` | none (no `UserPromptSubmit`) |
| Esc mid-goal | `Interrupt`; returning `decision: block` → "invalid interrupt hook JSON output", goal paused |
| `/goal resume`, goal finished | `Stop` |
| plain prompt "reply with the single word ok" | `UserPromptSubmit` (control: the event works, just not for `/goal`) |

Side finding for eshu: its `.codex/hooks.json` routes `/goal consent …` through
`UserPromptSubmit`, which Codex 0.160.0 does not fire for `/goal` lines. That
router probably never runs under Codex. eshu's problem to confirm, not fleet's.

## Muse hooks are a plugin package

Read from Muse Code 1.4.2's own CLI (`muse plugins --help`) and the text
embedded in its binary, then checked with `muse plugins validate`, which
neither installs nor runs anything (2026-10-02). Nothing was installed;
`~/.config/muse/auth.json` was not opened.

- **Two places hooks come from.** Project hooks: `.muse/hooks.json` in the
  repo, in the same shape as Codex's `hooks.json`, loaded only in a workspace
  the user trusted (eshu ships one). User-level hooks: there is no user
  `hooks.json`; they come from a **plugin package** installed with
  `muse plugins install <path> --scope user` (source tier `user-local`).
- **Events** (`HookEventKind`): `SessionStart`, `UserPromptSubmit`,
  `PreToolUse`, `PermissionRequest`, `PostToolUse`, `PreLLMCall`,
  `PostLLMCall`, `PreCompact`, `PostCompact`, `SubagentStart`,
  `SubagentStop`, `Stop` (can request continuation), `SessionEnd`,
  `Notification`, `PostToolUseFailure`, `StopFailure`, `PostToolBatch`,
  `Interrupt`, `SessionFork`. `PermissionRequest`, `Notification` and
  `Interrupt` would let the agent see Muse prompts and pauses through hooks
  rather than the screen alone (E6, E11).

**Package families.** Muse reads three manifest families:

| Package tested with `muse plugins validate` | Result |
|---|---|
| native `.muse-plugin/plugin.json` with a `Stop` hook (`{id, event, command[], timeoutMs}`) | valid, compatibility `full` |
| `.claude-plugin/plugin.json` plus `hooks/hooks.json` in the Codex/Claude shape | **valid, compatibility `full`**; family `claude-compatible` |
| `.codex-plugin/plugin.json` with hooks | rejected: "hooks/MCP/commands are not imported in this phase" (skills only) |

Codex itself reports `plugin_hooks` as removed (`codex features list`,
0.159.2), so Codex hooks stay in `~/.codex/hooks.json`. The shared piece is
the Codex `hooks.json` format: fleet installs it as `~/.codex/hooks.json`,
inside `~/.claude/settings.json`, and as the `hooks/hooks.json` of a
Claude-compatible plugin for Muse.

**Auto-update.** The `muse` launcher updates the binary in the background
(it moved this machine to 1.4.2 on 2026-10-02) unless `MUSE_NO_AUTO_UPDATE=1`
is set. Preflight reports the setting; parser drift is handled by
`ParserSupport.parsing_live` ([ADR 0018](../adr/0018-hub-api-and-contract-amendments.md)).

## Trust that survives upgrades (proposed)

Codex and Muse both re-ask for trust when a hook definition changes, and what
Codex's hash covers is not documented. Proposed: every fleet hook command is a
fixed string that calls the agent binary, `fleet-agent hook <event>
--harness <h>`, with no script file. The goal logic lives in `fleet-agent`, so
upgrading fleet changes no hook definition and the user approves once per
machine. The scripts directory above then holds nothing; the version check in
step 3 runs `fleet-agent hook --version`.

## Open questions

- Generic goal file location that every harness sandbox can write (Codex
  `workspace-write` limits writes to the workspace) without dirtying the
  checkout: candidates are a path under `.git/` or a git-ignored path the
  agent adds to `.git/info/exclude`. Needs a probe per harness.
- Muse `Stop` payload and continuation output (the input fields and how to
  say "keep going"). One live probe: install the test plugin at user scope,
  approve it, run `muse plugins hook test fleet-goal-hooks:<hook> --fixture`,
  remove it. Needs the owner present because it changes the user's Muse setup.
- Whether a Claude-compatible plugin package also installs as a Claude Code
  plugin (would replace editing `~/.claude/settings.json`). Not tested.
- ~~Muse hook support~~ Answered above: user-level hooks are a plugin package.
- ~~Whether Codex trust survives a script change~~ Avoided by design if the
  proposal above is accepted: the definition never changes.
