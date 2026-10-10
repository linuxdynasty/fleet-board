# 1d. Goal hooks (G1)

How fleet-board installs and uses harness hooks for goals. Capability G1 in
[01-agent-capabilities.md](01-agent-capabilities.md); contract
`EnsureGoalHooks` / `GoalHooksState` in [02-contracts.md](02-contracts.md).

## What the hooks are for

The loop itself is each harness's native `/goal` ([01b](01b-harness-commands.md#goal-subcommands)):
Claude, Codex and Muse (and Cursor, to be confirmed by plan item 0.21) keep
working toward the objective without any fleet hook.
Fleet's hooks do three narrower jobs:

1. **Session mapping (E9).** Record the harness session id against the
   pane's registry entry, so G2 and K3 can find goal files and transcripts.
2. **Turn-end events (E8).** Tell the agent a goal turn ended, with the
   harness's last message, so stuck and done detection does not depend on the
   screen alone.
3. **Stop guard.** Refuse a stop while the goal file says the goal is
   unfinished and nothing is watching it (the same rule as eshu's
   `goal-continue.sh`: `DONE` → allow; `BLOCKED … WATCH=<live pid>` → allow;
   `BLOCKED … OWNER=<who> ACTION=<what>` → allow; `BLOCKED` with neither (or a
   dead pid) → refuse and escalate).

They never type into the pane, never answer prompts and never resume a goal
the developer interrupted.

## Scope: user level, inert outside fleet goals

Decided 2026-10-02: hooks are installed at **user level** (the developer's own
harness config), not into each repo.

| Harness | File | Events used |
|---|---|---|
| Claude Code | `~/.claude/settings.json` | `SessionStart`, `UserPromptSubmit`, `Stop` |
| Codex | `~/.codex/hooks.json` | `SessionStart`, `Stop`, `Interrupt` (notify only) |
| Muse | a plugin installed at user scope: `muse plugins install <bundle> --scope user` (see [Muse](#muse-hooks-are-a-plugin-package)) | `SessionStart`, `Stop` (probed), `Interrupt` |
| Cursor (`cursor-agent`) | `~/.cursor/hooks.json` | to be decided by plan item 0.23; `beforeSubmitPrompt` and `stop` fire ([probe](#cursor-hooks-probe-2026-10-05)) |

- Every fleet hook exits 0 with no output unless it is running for a fleet
  goal, so the developer's own sessions, including ones on the same machine,
  see no change. **Claude Code, Codex and Muse use one mechanism, the agent's
  goal registry** ([02h](02h-goal-binding-and-pane-signals.md#goal-registry), ADR
  0019; Cursor's hook mapping is plan item 0.23): `StartGoal` writes `(pane, worktree path, harness session id)` before
  the goal is typed, and the hook asks the agent. A running harness cannot gain
  an environment variable, so in a reused pane `FLEET_GOAL_ID` is absent or the
  previous goal's, and is only a fast path for a window the fleet just created.
  How the hook finds its entry differs by harness:
  - **Claude Code and Codex:** the pane environment reaches hook commands (in
    the probe the hooks wrote to `$HOOK_LOG`, set only in the tmux pane), so
    `TMUX_PANE` identifies the pane and the pane's registry entry is the goal.
  - **Muse:** the pane environment does **not** reach plugin hooks (probe
    below); Muse sets only its own `MUSE_PLUGIN_*` and `CLAUDE_PLUGIN_*`
    variables. The hook reads `cwd` and `session_id` from its stdin payload:
    `session_id` matches an entry once learned; until then a `cwd` at or under
    an entry's worktree matches only when exactly one Muse pane is registered
    for that checkout. In `REPO_DRIVER` mode the registered path is the main
    checkout and is never the match key. Anything else exits 0 silently.
  - The first `UserPromptSubmit` or `PreToolUse` after registration records the
    harness session id (E9), since `SessionStart` fired before the goal existed.
- The hook scripts live under the agent's data directory
  (`~/.local/share/fleet-agent/hooks/<harness>/`); the settings entries only
  point at them. The command string stays fixed across upgrades and the script
  carries its version.
- **One hook definition for Claude Code, Codex and Muse** (Cursor's own
  `hooks.json` shape differs; plan item 0.23). Claude's `settings.json` hooks,
  Codex's `hooks.json` and Muse's Claude-compatible plugin `hooks/hooks.json`
  share one JSON shape (`{"hooks": {"<Event>": [{"matcher", "hooks": [{"type":
  "command", "command"}]}]}}`). The agent generates the three files from one
  definition; only the location differs.
- **The one place the agent writes harness settings** (v1).
  [ADR 0004](../adr/0004-agent-connectivity.md) says the agent never opens
  harness config. [ADR 0024](../adr/0024-pre-build-owner-decisions.md)
  (decision 3) allows exactly this exception: the agent may **add, update and
  remove only its own named hook entries** (the ones whose command points into
  the fleet hooks directory) in the user-level settings files of the table above.
  It never reads any other key (it parses the file only to find its own entries
  and keeps every other entry and its order), never writes
  file contents to a log, a snapshot, an event or the audit trail (only the
  path, the backup name, `hook_hash` and the verdict), and always backs the file
  up first. It never edits trust lists, auth or credential files, and it never
  answers the `/hooks` or `muse plugins approve` step.
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
2. **Merge, don't replace** (the ADR 0024 rule above). Read the user file, back it up to
   `<file>.fleet-backup.<timestamp>`, add or update only entries whose command
   points into the fleet hooks directory, keep every other entry and its order,
   write atomically (temp file + rename). Unparseable file → stop with
   `HOOKS_INSTALL_FAILED` and leave it untouched.
3. **Verify.** Run each script with a synthetic payload and no registry entry
   (must print nothing, exit 0), then with a throwaway one (must emit the
   expected event). For Muse the two cases are a `cwd` outside and inside a
   registered worktree, run through `muse plugins hook test <plugin>:<hook>
   --fixture <file>`; the fixture is `{"event": "Stop", "stdin": {…payload…}}`
   and `hook test` passes neither the caller's environment nor an `env` field
   to the hook. Report `hook_version`, `hook_hash` (sha256 of scripts plus
   entries), `verified`.
4. **Codex trust.** Codex runs a new or changed hook only after the user
   approves it in `/hooks` (trust is recorded per hook hash). `/hooks` is on
   the deny list ([01b](01b-harness-commands.md)) and the agent never passes
   `--dangerously-bypass-hook-trust`, so `GoalHooksState.trust = PENDING`
   goes to Triage: "approve fleet hooks once in Codex `/hooks` on <machine>"
   (`trust_approve_command` names the command; `UNKNOWN` is never read as
   trusted).
   Goals still run without trust; only E8 and the Stop guard are missing.
   **Muse** works the same way: an installed plugin's hooks run only after
   the user runs `muse plugins approve fleet-goal-hooks`, and trust is
   recorded per definition hash (`trusted_definition_hash`), so a changed hook
   prompts again. The agent never runs `approve`; the same `trust` Triage item
   names the command.

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
  their own `CONSENT:` handling; the agent reads it (G2) and writes nothing
  (`StartGoal` skips its `write_goal_file` step when `repo_ships_own` is true).
- **Generic file location (decided 2026-10-10, ruling on #57):**
  `<checkout top>/.fleet-board/goal-<goal_id>`. The pane's folder is the
  checkout top for every fleet-started goal ([03c](03c-goals.md),
  [02h](02h-goal-binding-and-pane-signals.md)). Step 12 creates the folder and
  `.fleet-board/.gitignore` (`*`), so the folder ignores itself; the agent
  never writes under `.git/` or edits any file it did not create. Rules:
  [safe writes and cleanup](01d2-goal-file-safe-writes.md#safe-writes).
  Probe: [below](#goal-file-location-probe-2026-10-10). The model gets the
  relative path `.fleet-board/goal-<goal_id>` in the objective text. The
  folder is named `.fleet-board`, not `.fleet`, because a short generic name
  may collide with another tool (recollection, not proof). In a scratch folder
  (F7) the same folder is made at the top of the scratch folder.
- **Claude Code `default` permission mode** asks for approval on every file
  edit, whatever the place. That is a harness fact (probed, table below), not a
  bug: a goal in that mode waits on each goal-file update, and Triage shows
  the prompt ([03d](03d-triage-sessions-logs.md)). Known limit.

## Interrupts

| Harness | Hook sees the interrupt? | Agent learns it from |
|---|---|---|
| Claude Code 2.1.286 | no: `Stop` does not fire on Esc | screen: "Interrupted · What should Claude do instead?" |
| Codex 0.160.0 | `Interrupt` fires; output is ignored ("invalid interrupt hook JSON output" when it tried to block) | `Interrupt` hook event plus screen "Goal paused" |
| Muse 1.4.2 | an `Interrupt` hook event exists (binary, not yet probed live) | screen: "goal paused by interrupt" |

An interrupt is the developer's decision. The agent reports a
`HarnessCondition` (E12) of kind `GOAL_INTERRUPTED` (or `GOAL_PAUSED`) with
`pause_cause` (`DEVELOPER_INPUT` when pane input just preceded it) and, for
Codex and Muse, a `HOOK_EVENT_KIND_INTERRUPT` event. The hub moves the goal to
`PAUSED{OWNER_AT_MACHINE}`, opens a Triage item "goal interrupted — resume?" and
never sends the corrective nudge. Resuming is the owner's: the harness's own
command from `goal_control` (`continue` for Claude, `/goal resume` for Codex
and Muse). Nothing resumes automatically.

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

## Muse Stop probe (2026-10-02)

Muse Code 1.4.2 on the hub MacBook. A Claude-compatible plugin
(`.claude-plugin/plugin.json` plus `hooks/hooks.json` with one `Stop` entry,
the format fleet would ship) was installed with `muse plugins install --scope
user` and approved with `muse plugins approve`. Muse ran in a throwaway tmux
server (`-L museprobe`) in an empty scratch folder with `PROBE_TOKEN` set in
the pane. The hook logged its stdin and, on the first stop of a turn,
answered `{"decision": "block", "reason": "…"}`. Every screen was read before
a key was sent.

| Step | Result |
|---|---|
| hook gated on `PROBE_TOKEN`, first prompt | hook ran (`hook_run_terminal` `completed`, exit 0) but logged nothing: the pane variable did not reach it |
| hook logs unconditionally, second prompt | two `Stop` runs in one `turn_id` |
| first `Stop`, hook answers `block` | Muse records `status: blocked`, adds the reason to the model's context as a `hook:stop` block, and runs another model turn |
| second `Stop` | `stop_hook_active: true`; hook allows; turn ends |
| script edited while approved | still `trusted_enabled`, same `source_digest`, no new prompt |

**Payload** (`Stop`): `hook_event_name`, `stop_hook_active`,
`last_assistant_message`, `session_id`, `turn_id`, `cwd`, `transcript_path`
(`null` in this build), `model`, `permission_mode`, `model_provider`. Because
`transcript_path` is null, K3 finds Muse transcripts by `session_id` under
`~/.local/share/muse/sessions/<date>/<session_id>/session.jsonl`.

**Environment seen by the hook:** `MUSE_PLUGIN_ROOT`, `MUSE_PLUGIN_ID`,
`MUSE_PLUGIN_DATA_DIR`, `CLAUDE_PLUGIN_ROOT`, `CLAUDE_PLUGIN_DATA` (both roots
point at Muse's cached copy of the package). No pane variables.

**Continuation works like Claude's:** `decision: block` plus `reason` keeps
the turn going, and `stop_hook_active` tells the hook a block already
happened, so the fleet Stop guard must allow the stop when it is `true` to
avoid a loop. The model in the probe still answered "OK" because the prompt
demanded exactly that word; the reason reached its context as a separate
block.

**Cleanup:** tmux server killed, plugin removed (`muse plugins list` empty),
`~/.config/muse/trust.json` restored byte for byte (trusting the scratch
folder had added one entry), `settings.json` unchanged, `auth.json` never
opened. Two small prompts were spent; their session log stays in the owner's
Muse history.

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

## Cursor hooks probe (2026-10-05)

cursor-agent 2026.10.01-e373342 on the hub MacBook, throwaway tmux server
`-L probe-cursor`, a scratch clone of this repo at 843df01 (its project
`.cursor/hooks.json` from #87 and #90), and `FLEET_CURSOR_HOOK_LOG` set so the
dev adapter logged event names, key names and ids only. Two prompts: a one-word
reply, then one asking a Task helper to read `README.md`.

| Step | Events logged |
|---|---|
| prompt 1 | `beforeSubmitPrompt`, then `stop`; `session_id` equals `conversation_id` |
| prompt 2, parent | `beforeSubmitPrompt`, then `stop`, same ids as prompt 1 |
| prompt 2, the helper's file read | `postToolUse` under a **new** `conversation_id` (equal to its `session_id`), with no `subagent_id` and no `parent_conversation_id` |
| helper start | **no `subagentStart`**, although it was wired |

- The `stop` payload carries `input_tokens`, `output_tokens`,
  `cache_read_tokens`, `cache_write_tokens`, `loop_count` and `status`.
- A helper's events carry `conversation_id`, `cursor_version`, `duration`,
  `generation_id`, `hook_event_name`, `model`, `session_id`, `tool_input`,
  `tool_name`, `tool_output`, `tool_use_id`, `transcript_path`, `user_email`,
  `workspace_roots`. `user_email` is personal data: a fleet hook drops it.
- So in this version a helper cannot be tied to its parent from hook data, and
  its transcript is a separate top-level folder. A fleet goal hook keys on the
  parent's own events and ignores a helper's.
- Not checked: `/goal` lines reaching `beforeSubmitPrompt`, whether `stop` can
  hold a turn for a fleet goal (item 0.23), and the Cursor IDE.

Cleanup: tmux server killed; `~/.cursor/hooks.json` byte-identical to its
baseline; the two probe project folders under `~/.cursor/projects/` and the
scratch clone removed; the log deleted once recorded here. The probe also took a
baseline copy of `~/.cursor/cli-config.json` and compared the file with it
after: only two caches Cursor rewrites on every run had changed, left as they
were. That comparison read the file's contents. Only later was it found to hold
an authInfo section as well as settings; whether the comparison showed any of
its values was not recorded. The copy was deleted on 2026-10-05 without being
opened again, and 01b now lists the file as one the agent never reads. No other
credential file was opened.

## Goal-file location probe (2026-10-10)

Question: where can the agent write a goal file that the model can later edit
from inside each harness's sandbox, without the checkout showing a change?
Claude Code 2.1.296, Codex 0.160.0 and Muse Code 1.4.4 on the hub MacBook (tmux
3.7c). Throwaway tmux server on its own socket (`-S`, `-f /dev/null`), a real
linked worktree of this repo as the working directory, one small prompt per
harness, every screen read before the next key. Each prompt asked the model to
run (or write with the file tool) one tiny file at four places: (1) `.fleet/goal` in the
worktree (the literal path probed; the decided folder name `.fleet-board`
differs only in that name, and `.fleet-board/` itself was not TUI-probed),
(2) a folder in the worktree's git directory (for a linked worktree
that is `<main checkout>/.git/worktrees/<name>/`, outside the worktree), (3) a
file in `/tmp`, (4) a folder under the home folder. Sandbox settings were the
harness's own flags for one run: Codex `-s workspace-write -a on-request`,
Claude Code `--permission-mode default|acceptEdits` and, for the sandbox,
`--settings` with `sandbox.enabled` and `autoAllowBashIfSandboxed` (the second
setting is what removed the Bash prompts; without it Claude Code still asks), Muse's default (sandbox on, approval
`on-request`) with `--trust-workspace`.

| Place | Codex 0.160.0 `workspace-write` | Claude Code 2.1.296 sandbox on + `autoAllowBashIfSandboxed` | Claude Code, permission prompt only | Muse Code 1.4.4 sandbox on | Cursor, OpenCode |
|---|---|---|---|---|---|
| `<worktree>/.fleet/goal` | written, no prompt | written, no prompt | `acceptEdits`: no prompt. `default`: one approval per write | written, no prompt | not probed; hypothesis: the same cwd rule |
| worktree git dir (outside the worktree) | `Operation not permitted` | written | prompt, even in `acceptEdits` | written | not probed; hypothesis: the same cwd rule |
| `/tmp/<file>` | written | `operation not permitted` (only `/tmp/claude` and the per-user temp folder) | prompt | written | not probed; hypothesis: the same cwd rule |
| `~/.local/share/<dir>` | `Operation not permitted` | `Operation not permitted` | prompt, even in `acceptEdits` | `Operation not permitted` | not probed; hypothesis: the same cwd rule |

Only `.fleet/` inside the working directory was writable in Claude Code, Codex
and Muse. In Claude Code it was the only place that needed no approval in
`acceptEdits` with no `--settings` flag (the "permission prompt only" column:
only `--permission-mode` was set). The sandbox column did use `--settings`, for
that one run, so it is not the pass-through case. The probe also checked
`git status --porcelain` with a `/.fleet/` line in `info/exclude` (plain repo
and linked worktree, in a scratch repo). That mechanism was **rejected** by the
ruling, because the agent may not edit files it did not create; the
self-ignoring folder replaces it
([check](01d2-goal-file-safe-writes.md#self-ignoring-folder-check-2026-10-10)).

Extra findings:

- **Codex marks `.git/` read-only when `.git` sits directly under the
  writable root.** `codex sandbox -P :workspace -C <plain repo>` (the sandbox
  command, which starts no model; **not** run in the TUI) refused a new file
  under `.git/fleet/` and under `.git/info/`, and accepted `.fleet/` next to
  it. The carve-out is only for `<cwd>/.git`: from `-C <repo>/sub/deep` the
  same command wrote `../../.git/info/fleet-test`,
  `../../.fleet-board-top-test`, `../../.fleet-board/goal-top` and a file in
  `/tmp` (rc=0), while from `-C <repo>` the `.git/info` write got "Operation
  not permitted" (arbiter run, 2026-10-10). The ruling does not rely on this;
  it never writes under `.git/` at all.
- **Codex refuses a workspace reached through a symlink** (also from
  `codex sandbox`, not the TUI). With the repos
  folder reached through a symlink, `codex sandbox -P :workspace` stopped with
  "writable root … contains symlink component …; symlinked writable roots are
  not supported". The TUI runs used the real path. `StartGoal` resolves the work folder to its
  real path before it starts the window and the registry stores that path
  ([02h](02h-goal-binding-and-pane-signals.md#startgoal-steps)); not re-run in
  the TUI.
- The probe forced the strictest sandbox of each harness on purpose; a
  developer's day-to-day settings may be looser. The goal file must work in
  both.
- `git worktree remove` works with the ignored folder present, and
  `git clean -fdx` deletes it (without `-x` it stays). Both were checked by the
  arbiter on `.fleet-board/` with its own `.gitignore`, not on `.fleet/`
  ([check](01d2-goal-file-safe-writes.md#self-ignoring-folder-check-2026-10-10)).
  The goal file is not durable state: the hub and the agent keep the goal, the
  file is a working copy.
- Codex's first reply to the multi-path prompt claimed three paths failed
  without having run them; only a second prompt produced the real errors, and
  the files on disk were the check. Judge a probe by the disk, not by the
  model's report.
- A model write needs an approval in Claude Code `default` mode wherever the
  file lives, so a goal run in that mode waits on every goal-file update.
  **Hypothesis (not probed):** the Stop guard and E8 still work in that mode,
  because hooks run outside the sandbox; no hook read the file in this probe.

**Not proved:** a plain (non-worktree) repo under Claude Code and Muse
(**hypothesis, not probed:** the sandboxes decide by the working directory; the
one plain-repo point measured is Codex); Claude Code's edit tool (only its write
tool and Bash were run); hooks reading the file (hooks run outside the sandbox,
not probed here); Windows; Cursor and OpenCode (not probed; hypothesis: the same
working-directory rule). A pane started in a subfolder of a repo no longer
matters: a fleet-started goal always starts at a checkout top, and a pane in a
subfolder is `SESSION_WORKDIR_MISMATCH`
([03c](03c-goals.md), [02h](02h-goal-binding-and-pane-signals.md)). A harness
that still refuses the write ends as `GOAL_FILE_NOT_SEEN` and stuck detection.

**Cleanup:** the tmux server was killed and its socket deleted; the probe files
under the worktree, its git dir, `/tmp` were removed and the scratch repo was
deleted (removal checked on disk); a home-folder write was tried and refused
in all three, so nothing was written there. No trust
entry was saved by hand: Codex asked about trust in the scratch repo, and
Muse in the worktree and in the main checkout; each was answered "Quit". The
worktree showed no trust question in Codex or Claude Code. Muse ran with
`--trust-workspace` (its help says it does not save trust; taken from the help
text, not tested). Muse's trust file kept its old modification time
(September), so no entry was added. **Codex's trust state is unproven:** its
config file, which holds its trust entries, changed modification time during
the probe window, for a reason not established (contents never read), and no
checksum was taken before the probe, so there is no baseline. The Codex daemon and update prompts were answered "Run without daemon
this time" and "Skip". `~/.claude.json`, `~/.codex/auth.json`,
`~/.codex/config.toml`, `~/.config/muse/` and the keychain were never opened.
About nine short prompts were spent; their session logs stay in the owner's
Claude Code, Codex and Muse history.

## Trust that survives upgrades (proposed)

Codex and Muse both re-ask for trust when a hook definition changes, and what
Codex's hash covers is not documented. Proposed: every fleet hook command is a
fixed string that calls the agent binary, `fleet-agent hook <event>
--harness <h>`, with no script file. The goal logic lives in `fleet-agent`, so
upgrading fleet changes no hook definition and the user approves once per
machine. The scripts directory above then holds nothing; the version check in
step 3 runs `fleet-agent hook --version`.

Evidence for Muse ([Stop probe](#muse-stop-probe-2026-10-02)): with the hook approved, the script it pointed
to was edited in place; Muse kept the hook `trusted_enabled` with the same
`source_digest` and ran the edited script without asking. Muse's trust covers
the hook entry in `hooks/hooks.json`, not a script the entry points to by
path. Codex is still unprobed on this point; the proposal does not depend on
it, because the entry never changes either way.

## Open questions

- ~~Generic goal file location that every harness sandbox can write~~
  Answered 2026-10-10 (ruling on #57): `<checkout top>/.fleet-board/goal-<goal_id>`
  in a self-ignoring folder ([decision](#who-writes-the-goal-file),
  [probe](#goal-file-location-probe-2026-10-10),
  [safe writes](01d2-goal-file-safe-writes.md)). The subfolder case is dissolved
  by rule and the Claude Code `default` mode limit is recorded above.
- ~~Muse goal registry by `cwd`~~ Answered by the registry design: entries
  match by pane and `harness_session_id`; `cwd` is only the bootstrap until the
  first hook after registration records the session id. Still to confirm:
  `UserPromptSubmit` fires for the first turn of a plugin-hooked Muse session.
- ~~Muse `Stop` payload and continuation output~~ Answered by the Muse Stop
  probe above.
- Whether a Claude-compatible plugin package also installs as a Claude Code
  plugin (would replace editing `~/.claude/settings.json`). Not tested.
- ~~Muse hook support~~ Answered above: user-level hooks are a plugin package.
- ~~Whether Codex trust survives a script change~~ Avoided by design if the
  proposal above is accepted: the definition never changes.
