# 1d4. Muse `SessionStart` timing (G1)

Plan item 0.13 (issue #58), decided 2026-10-12: when Muse fires its
`SessionStart` hook, and whether it is early enough to use as the
fallback for the session-id registry (the small list that ties a goal
to a harness session). Parent spec:
[01d](01d-goal-hooks.md); the registry is in
[02h](02h-goal-binding-and-pane-signals.md#goal-registry).

## Decision

- **`SessionStart` stays inert.** It fires exactly once per session,
  lazily at the first prompt submission, in the same second as the
  first `UserPromptSubmit`, and never refires. It therefore arrives no
  earlier than the signal E9 already uses, and in a reused pane it
  never fires at all. Same ordering as every other harness (and as
  Cursor's `sessionStart`, [01d3](01d3-cursor-goal-hooks.md)).
- **E9 keeps the first-`UserPromptSubmit` rule** for Muse: the first
  `UserPromptSubmit` after registration records the harness session
  id. This probe also confirms that `UserPromptSubmit` fires for the
  first turn of a plugin-hooked Muse session, which 01d listed as
  still to confirm.

## Probe record (2026-10-12)

Muse Code 1.4.7 (1.4.7-R6101.1) on the hub MacBook. The launcher
auto-updated from 1.4.6-R6000.1 to 1.4.7 at the start of the probe, so
every observation below is on 1.4.7. A Claude-compatible plugin
(`.claude-plugin/plugin.json` plus `hooks/hooks.json` with one
`SessionStart`, one `UserPromptSubmit` and one `Stop` entry, each
logging its stdin payload and a timestamp) was installed with
`muse plugins install --scope user` and approved with
`muse plugins approve` (all three `trusted_enabled`, checked with
`muse plugins inspect`). Muse ran in a throwaway tmux server
(`-L muse-sess58`) in an empty scratch folder with
`muse --trust-workspace`. Baselines: checksums of
`~/.config/muse/trust.json` and `settings.json` plus the plugin list
(empty). Every screen was read before the next key.

| Step | Observed |
|---|---|
| TUI start 20:41:19 | ready prompt, no trust box; **no `SessionStart` in the log** |
| `hook test` fixture (control) | `SessionStart` logged: the command works, the live session simply had not fired it |
| prompt 1 `reply with the single word ok`, submitted 20:44:41 | `SessionStart` (`source: startup`) and `UserPromptSubmit` logged **in the same second** with the same `session_id`; model answered "ok"; `Stop` at 20:44:46 (`stop_hook_active: false`) |
| prompt 2, same wording | `UserPromptSubmit` and `Stop` with the same `session_id` and a new `turn_id`; **`SessionStart` did not refire** |

- **Payloads.** `SessionStart`: `hook_event_name`, `source`
  (`startup`), `session_id` (stable across both turns), `cwd`
  (`/private/tmp/…`, the real path), `transcript_path` (`null` in
  this build, so K3 keeps keying Muse transcripts by `session_id`
  as 01d says), `model` (`muse-spark-1.3`), `permission_mode`
  (`default`), `model_provider` (`meta`). `UserPromptSubmit` adds
  `prompt` and `turn_id` (new per turn). `Stop` adds
  `stop_hook_active` and `last_assistant_message`.
- **Environment.** Not re-logged; the Stop probe's finding stands
  (only Muse's own `MUSE_PLUGIN_*` / `CLAUDE_PLUGIN_*` variables,
  no pane variables).
- **Cleanup.** Server killed; plugin removed (`muse plugins list`
  empty); `trust.json` byte-identical to baseline (untouched);
  `settings.json` reordered by the binary during the auto-update,
  restored byte for byte from the baseline and verified;
  the scratch folder and the hook log deleted once recorded here.
  `auth.json` and the keychain never opened. Two small prompts and
  one `hook test` (no model cost) spent; their session log stays in
  the owner's Muse history.

## Side findings for sibling items (not this decision)

- Each typed line twice needed a second Enter to submit: the first
  Enter left the text in the input box, the second submitted it.
  The Cursor probe saw the same quirk
  ([01d3](01d3-cursor-goal-hooks.md#side-findings-for-sibling-items-not-this-decision)).
  Judged by the hook log, not by the screen.
- The launcher auto-updated mid-probe (1.4.6 to 1.4.7) before the
  first screen. Plan item 0.4 (`MUSE_NO_AUTO_UPDATE=1`) is the
  standing answer for fleet machines.

## Still open

- `SessionStart` on `muse resume` or a session fork, and
  `SessionEnd` timing (not wired in this probe).
- Whether a plugin installed but not yet approved behaves
  differently on first run (this probe approved before starting).
