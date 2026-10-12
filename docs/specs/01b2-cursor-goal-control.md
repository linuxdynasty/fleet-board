# 1b2. Cursor goal control (G3, G4)

Plan item 0.21 (issue #98): the syntax of `/goal` in `cursor-agent`,
pause, resume, the done signal, re-arm after done, behaviour after a usage
limit, and whether `beforeSubmitPrompt` sees `/goal` lines. Parent spec:
[01b](01b-harness-commands.md#goal-subcommands) (the goal tables carry the
mapping); hooks: [01d](01d-goal-hooks.md).

Probed 2026-10-12 on `cursor-agent` 2026.10.01-e373342 (macOS, tmux 3.8),
throwaway tmux server (`-L probe98`, `-f /dev/null`), an empty scratch
folder with project hooks (`.cursor/hooks.json`) that logged hook event
names, prompt lengths, a `/goal`-prefix flag and `stop` fields only.
Eight small model prompts spent; every screen read before the next key.

## What the builtin skill says

`/goal` is a builtin skill ("Set a goal that Cursor will pursue to
completion", `~/.cursor/skills-cursor/goal/SKILL.md`, synced 2026-10-04).
Its rules, as shipped:

- Accept `/goal <objective>`. There are no subcommands: no pause, resume,
  clear, done, status or edit. An empty objective should show
  "Usage: /goal <objective>".
- No deadline, token budget or turn budget; a goal continues until it is
  complete. Time limits (`30m`) are refused and recurring work belongs to
  `/loop`.
- Start calls `CreateGoal` once; finishing calls `UpdateGoal` with status
  "complete". `UpdateGoal` is otherwise only for resuming a goal the user
  paused.

The probe below tests each of these against the running harness. One rule
did not hold: a bare `/goal` shows no usage text (see the table).

## Probe steps

| Step | Observed |
|---|---|
| `/goal` typed, no Enter | menu: `/goal [objective]`, "Start a durable goal that continues while idle"; second line: `/goal`, "Set a goal that Cursor will pursue to completion. (builtin skill)" |
| `/goal ` (trailing space), no Enter | menu closes, no subcommand hints |
| bare `/goal`, Enter (twice, separate tries) | silent no-op both times: line clears to the placeholder, no output, no hook fires, no model call |
| plain `reply with the single word ok` | control: answered "ok"; `beforeSubmitPrompt` fired, then `stop` (`status` completed, token counts present) |
| `/goal reply with the single word mango` | `beforeSubmitPrompt` fired with the `/goal` line; model ran `CreateGoal`, then `UpdateGoal`, printed "Created goal …" and "Updated goal complete", answered "mango" |
| `/goal` colours goal (red, green, blue, one per turn, wait between) | fresh "Created goal …" after the finished mango goal (re-arm works); answered "red", then 3 automatic "Continuing goal" turns while idle, then "Goal paused (9s)" |
| `go on` (plain message) | resumed: "green", 3 idle continuations, "Goal paused (16s)"; `beforeSubmitPrompt` fired for the message |
| second `go on` | "Updated goal complete", "blue", "That's all three colours … It ran for 16s in total"; one `stop`, no idle turns, no pause |
| `count from 1 to 30 …` | too fast to interrupt (finished in under 6 s) |
| long explanation prompt, Ctrl+C mid-stream | generation stopped; two `stop` events: `status` "aborted" then "error", both with no token counts; the prompt text returned to the input line |
| `continue where you stopped` (after clearing the line) | answered (about the earlier counting task, not the interrupted one: resumption fidelity is the model's context handling, not a harness guarantee) and ended normally; resumption is a normal turn, not a harness resume |

## Findings for the goal tables

- **Syntax:** `/goal <objective>` only. A bare `/goal` is swallowed
  client-side: it never reaches `beforeSubmitPrompt`, so unlike Codex a
  hook cannot treat it as a status check.
- **Start:** `CreateGoal` ("Created goal …" on screen).
- **Idle continuation:** while work remains, Cursor re-prompts itself
  ("Continuing goal") without firing `beforeSubmitPrompt`; each turn ends
  with a normal token-carrying `stop`.
- **Pause:** when the goal stalls, the screen shows "Goal paused (Ns)"
  with Ns counting up, and a `stop` with no token counts fires. A user
  interrupt (Ctrl+C; the input line hints "ctrl+c to stop") fires
  `stop` "aborted" then `stop` "error", also token-less, and puts the
  prompt text back on the input line.
- **Resume:** any plain message resumes a paused goal
  (`beforeSubmitPrompt` fires, normal turns follow). There is no resume
  subcommand. Esc during a turn was not tested.
- **Done:** `UpdateGoal` complete ("Updated goal complete" plus the total
  elapsed time). Only then do the idle continuations stop.
- **Re-arm:** a new `/goal <text>` after a completed goal arms a fresh
  goal in the same session.
- **Usage limit:** not seen. Turns ran normally throughout; the skill
  file states there is no token or turn budget. Provoking a limit is out
  of scope (never use up the plan).

## Side findings for sibling items (not this record)

- After an interrupt the input line holds the old prompt; Ctrl+U cleared
  it back to the placeholder (for 0.19, #96; one observation only).
- After a turn the placeholder reads "Add a follow-up"; a fresh pane
  reads "Plan, search, build anything" (for 0.19).

## Still open (for row 30, #95)

- Which token-less `stop` values the adapter can trust as "paused" (all
  three seen: completed-without-tokens, aborted, error).
- Esc during a turn (only Ctrl+C probed).
- Behaviour at a real usage limit (by rule, unseen until it happens).

## Cleanup

Server killed. The scratch folder, the hook log and all captures deleted.
The probe's registry folder under `~/.cursor/projects/` (trust entry,
transcript, `worker.log`) removed; the project listing verified identical
to its baseline. `~/.cursor/hooks.json` verified byte-identical (never
touched). `cli-config.json`, `auth.json` and the keychain never opened.
Eight small model turns stay in the owner's Cursor history.
