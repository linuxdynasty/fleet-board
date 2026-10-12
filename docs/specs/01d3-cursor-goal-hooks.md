# 1d3. Cursor goal hooks (G1)

Plan item 0.23 (issue #100), decided 2026-10-12: which Cursor hook events
carry the goal protocol, whether `stop` can hold a turn, how a hook finds
its pane and goal-registry entry, and the user-level `~/.cursor/hooks.json`
entries under ADR 0024 decision 3. Parent spec: [01d](01d-goal-hooks.md);
probe record at the bottom.

## Decision

- **Events.** `beforeSubmitPrompt` carries session mapping (E9): it fires
  for plain prompts and for `/goal` lines, with the prompt text. `stop`
  carries turn-end events (E8) and the Stop guard. `sessionStart` and
  `sessionEnd` fire but are fire-and-forget and stay inert (a session
  starts before its goal exists, the same ordering as the other
  harnesses). `subagentStart` is wired but never fires for Task helpers
  in the CLI; a helper's tool events carry a new `conversation_id` with
  no parent link, so a fleet goal hook keys on the parent's own events
  and ignores a helper's.
- **The Stop guard holds through `followup_message`.** `stop` input is
  `{status, loop_count}`; when the output `followup_message` is a
  non-empty string, Cursor submits it as the next user message and the
  turn continues (probed). The guard answers with the guard reason while
  the goal file says the goal is unfinished, and with no output when the
  file says `DONE`, `BLOCKED … WATCH=<live pid>`, or
  `BLOCKED … OWNER=<who> ACTION=<what>`. A follow-up does not re-fire
  `beforeSubmitPrompt`. `loop_count` numbers the follow-ups from 0 and
  the per-script `loop_limit` (default 5) caps them, so a stuck guard
  ends the turn on its own; row 30 still allows the stop and escalates
  instead of holding forever.
- **The hook finds its pane like Claude Code and Codex.** The pane
  environment reaches hook commands (probed: `TMUX_PANE` is the pane's
  own id), so `TMUX_PANE` is the fast path and the pane's registry entry
  is the goal. `conversation_id` equals `session_id` and is stable
  across turns; the first `beforeSubmitPrompt` after registration
  records it (E9). Until then, `workspace_roots` at or under an entry's
  worktree matches only when exactly one Cursor pane is registered for
  that checkout (the Muse `cwd` rule with Cursor's key). The hook drops
  `user_email` (personal data) and anything outside the registry exits 0
  silently.
- **`beforeSubmitPrompt` never blocks.** Its output is
  `{continue, user_message}` and can refuse a submission; the fleet hook
  always continues, so goals and plain prompts submit exactly as without
  it.
- **User-level entries.** `~/.cursor/hooks.json`, shape
  `{"version": 1, "hooks": {"beforeSubmitPrompt": [{"command": …}],
  "stop": [{"command": …, "loop_limit": …}]}}`. User hooks run with cwd
  `~/.cursor/`, so the command is an absolute path into the agent's data
  directory and stays fixed across upgrades while the script carries its
  version. Cursor asks for no hook approval (unlike Codex `/hooks` and
  Muse `plugins approve`); hooks run in a trusted workspace. Merge rule
  from Cursor's docs: every source runs, and for `followup_message` the
  user-level answer wins over a project's. The `repo_ships_own` rule
  still applies first: a project `stop` hook or goal router on
  `beforeSubmitPrompt` makes that workdir inert, so two Stop guards
  never fight.

## Install, verify, uninstall (Cursor)

`EnsureGoalHooks` step 1 reads the workdir's `.cursor/hooks.json`; steps
2 to 4 of [01d](01d-goal-hooks.md#install-verify-uninstall) apply with
these Cursor cases: the verify pair is a synthetic `beforeSubmitPrompt`
and `stop` with no registry entry (silent, exit 0) and with a throwaway
entry (the expected event); there is no trust step, so `GoalHooksState`
for Cursor never reports `PENDING`. Uninstall removes only fleet
entries, as elsewhere.

## Probe record (2026-10-12)

cursor-agent 2026.10.01-e373342 on the hub MacBook, throwaway tmux
server (`-L probe100`), an empty scratch folder with project hooks
(`.cursor/hooks.json`) that logged redacted payloads, the hook cwd, and
two allowlisted variables. Three user prompts plus one hook-driven
follow-up; every screen read before the next key.

| Step | Observed |
|---|---|
| fresh folder start | "Workspace Trust Required" box; `[a]` trusted it |
| `reply with the single word ok` | `sessionStart`, `beforeSubmitPrompt`, `stop`; model answered "ok" |
| `/goal status` | `beforeSubmitPrompt` fired with the `/goal` line; the model explained `/goal` takes an objective and created nothing |
| `reply with the single word yes`, one-shot `stop` hook answering `followup_message` | model answered "yes"; `stop` (`loop_count` 0) held the turn; the follow-up arrived as the next user message with no new `beforeSubmitPrompt`; model answered "done"; `stop` (`loop_count` 1) ended the turn |
| `/quit` | clean exit, "resume with `agent --resume=<id>`", the id equal to the session's own |

- **Payloads.** Common fields: `conversation_id` (= `session_id`,
  stable), `generation_id` (new per user message), `model`,
  `hook_event_name`, `cursor_version`, `workspace_roots` (no `cwd`
  key), `user_email`, `transcript_path` (`null` in the CLI, so K3 keys
  Cursor transcripts by conversation id as [01b](01b-harness-commands.md#cursor)
  says). `beforeSubmitPrompt` adds `prompt` and `attachments`; `stop`
  adds `status`, `loop_count`, and the `*_tokens` counts.
- **Environment.** `TMUX_PANE` in hooks is the pane's own tmux id
  (checked against a second pane, `%2`); the harmless marker exported
  in the pane arrived too. Project-hook cwd is the project root.
- **Cleanup.** Server killed; the scratch folder, the hook log, the
  trust entry, the transcript, and the whole
  `~/.cursor/projects/<scratch>/` folder removed (project listing
  identical before and after); `~/.cursor/hooks.json` verified
  byte-identical (never touched); `cli-config.json`, `auth.json`, and
  the keychain never opened. Four small model turns spent; their
  session log stays in the owner's Cursor history.

## Side findings for sibling items (not this decision)

- `/goal` takes a bare objective with no `status` subcommand; with no
  goal the model says so and creates nothing (probed for 0.21, #98:
  [01b2](01b2-cursor-goal-control.md)).
- After a turn the input line showed the "Add a follow-up" placeholder,
  and a typed line twice needed a second Enter to submit (for 0.19,
  #96; observations only, nothing decided).

## Still open

- Tying a helper's events to its parent (no parent id anywhere; the
  Cursor IDE is out of scope per ADR 0027).
- Cursor interrupts (Esc): what the hook and the screen show (row 30).
