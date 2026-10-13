# Cursor screen fixtures

Real screens from `cursor-agent`, for the parser tests in
`internal/harness/cursor` (plan item 0.1, issues #25 and #180, row 30). Files
are in `2026.10.01/`.

| Field | Value |
|---|---|
| Harness version | `cursor-agent` 2026.10.01-e373342 (`cursor-agent --version`) |
| Captured | 2026-10-10 (`trust-workspace.txt`, `ready-trusted-child.txt`), 2026-10-12 (`ready-ansi.txt`, the `ready-input-*` and `ready-followup-*` files) and 2026-10-13 (`usage.txt`, `usage-not-shown.txt`), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
| Plan | Pro+ (shown on `/usage`; see 01b) |

## How it was captured

Protocol: the `fleet-live-probe` skill. A throwaway tmux server (own socket in
a short temp folder, `-f /dev/null`), killed at the end. The agent was started
by typing `cursor-agent` in a shell pane, as the developer does. Every screen
was read with `capture-pane -p` before the next key. No prompt was sent to a
model, so no quota was spent.

Keys sent: `q` to leave the trust box (the "no" answer, on purpose; the
default is "[a] Trust this workspace"). Nothing was typed into a Cursor pane
on 2026-10-10: until plan item 0.19 proved how to tell leftover input from the
fresh placeholder, every `SendKeys` purpose answered `PANE_BUSY` on a Cursor
pane (02h), probe sessions included. The 2026-10-12 item-0.19 probe (#96)
typed under the probe protocol instead: draft text (no Enter), `Ctrl-U`,
`Esc`, `Ctrl-C`, and four small prompts with separate Enters (see the rule
section below); every screen was read before each key.

## Files

| File | Screen | How reached |
|---|---|---|
| `trust-workspace.txt` | "Workspace Trust Required" box, "[a] Trust this workspace" / "[q] Quit" | start in a new folder |
| `ready-trusted-child.txt` | ready screen, no box: "→ Plan, search, build anything", footer with model, approval mode and context share | start in a child of an already-trusted folder |
| `ready-ansi.txt` | the ready screen with ANSI escapes | start in the main checkout (already trusted), idle, 2026-10-12 |
| `ready-input-has-draft.txt` | ready screen with unsent typed text (`→ hello-draftprobe`) | typed text, no Enter (plan item 0.19 probe, #96, 2026-10-12) |
| `ready-input-has-draft.ansi.txt` | the same input line with ANSI escapes: no faint attribute anywhere | same screen, `capture-pane -e` |
| `ready-followup-placeholder.txt` | post-turn input line (`→ Add a follow-up`) | after a completed turn (same probe) |
| `ready-followup-placeholder.ansi.txt` | the same input line with ANSI escapes: `→` and text faint | same screen, `capture-pane -e` |
| `usage.txt` | the `/usage` dialog: "Usage • Pro+", "Resets Nov 4", Included / Auto / API % used, On-Demand Disabled, "Esc to close" | `/usage` typed at the ready prompt, Enter separately, dialog read, Esc |
| `usage-not-shown.txt` | `/usage` typed but not submitted: the `/` command menu, no dialog | `/usage` typed at the ready prompt, no Enter; the usage parser must yield unknown on it |

## Redactions and clean-up

- Paths are `~/scratch`. The repo name on the ready footer is `a` and the
  branch is `main*`.
- Trailing spaces and trailing blank lines are trimmed.

## Input-line rule (item 0.19 decided 2026-10-12, #96)

Probed on `cursor-agent` 2026.10.01-e373342, macOS, tmux 3.8, a throwaway
server (`-L probe96`, killed after), start in a child of an already-trusted
folder (no trust box), four small prompts spent (`say ok`, `say bye`, a
count, a poem). Every screen read before each key.

- A plain capture (`capture-pane -p`) never tells the fresh placeholder
  (`→ Plan, search, build anything`, later `→ Add a follow-up`) apart from
  real text: both are `→ <text>`.
- A styled capture (`capture-pane -e`) does. The placeholder's `→` and text
  carry the faint attribute (`^[[2m`, seen as `^[[0;2m` runs); a typed draft
  has no faint attribute anywhere on the line
  (`ready-input-has-draft.ansi.txt` against `ready-ansi.txt` and
  `ready-followup-placeholder.ansi.txt`). The block cursor (reverse video,
  `^[[7m` or `^[[100m`) sits on the placeholder's first letter when empty
  and after the last letter of a draft.
- After each of four completed turns the input line held the faint
  `→ Add a follow-up` placeholder, never the old prompt: no leftover-text
  state was seen on the normal path.
- Clearing: `Ctrl-U` and `Ctrl-C` on an idle line each restored the faint
  placeholder (verified by a styled re-read). `Esc` on an idle draft did
  nothing. Typing replaces the placeholder from its first letter.
- Submit quirk (unproven mechanism): text and Enter sent in one `send-keys`
  call twice failed to submit (line back to placeholder, nothing in the
  transcript); text verified on screen, then a separate Enter, submitted.
  Send text and Enter separately with a settle read between.
- Wedge (unproven mechanism): after the `Ctrl-C`-on-idle-input test, later
  keystrokes stopped reaching the first agent process (idle CPU, placeholder
  screen); killing it and relaunching resumed the same chat. If input stops
  landing, restart rather than stacking keys.

Row 30 reads the rule from 01b and 02h; the `.ansi.txt` fixtures above are
its parser tests (both halves). A parser must read styled lines; on plain
lines every `→ <text>` is unknown, never empty.

## /usage dialog (row 30 D2, 2026-10-13)

Probed on `cursor-agent` 2026.10.01-e373342, macOS, two throwaway servers
(`-L probe95usage`, `-L probe95usage2`, both killed after), empty scratch
folders under `/tmp`, pane 200x50. Every screen read before each key. No
prompt sent to a model, so no quota spent.

- The trust box appeared in each new folder and was answered `a` on purpose
  (the default); both per-folder project dirs were deleted after (directory
  listing matches the baseline) and `hooks.json` kept its checksum.
- Typing `/usage` shows the `/` command menu first ("→ /usage: Show plan
  and on-demand usage"); a separate Enter opens the dialog
  (`usage-not-shown.txt` is the menu state). `Ctrl-U` cleared the typed line
  afterwards (faint placeholder back, verified by re-read).
- The dialog (`usage.txt`): title "Usage • Pro+" with "Resets Nov 4" at the
  right, "Monthly plan and on-demand usage", a `Category / Current / Usage`
  table with Included, Auto and API rows ("100% used" with block bars: the
  account sat at its ceiling), an "On-Demand: Disabled" row with an em-dash
  bar, the note "On-demand usage is off", a dashboard link and "Esc to
  close". Esc closed it. The dialog shows the plan only, no account identity.
- `cli-config.json` and `auth.json` were never opened. Raw pane captures
  were deleted after the redacted fixtures above were written.

## Not captured, and why

- A permission prompt or post-turn input line holding old text: typing into a Cursor
  pane followed the 0.19 probe protocol; no leftover-text state appeared, and
  mid-turn interrupt (Esc) is still open (row 30).
- `/usage` with On-Demand on, `/goal` subcommands, pause and resume
  (0.21), account identity (0.22), probing cost (0.8): see the "Not probed"
  list in 01b; several need quota spend or docs. (`/usage` with On-Demand
  off is `usage.txt` above.)
- Prompt, limit and error screens: they need a model call or a real limit,
  and the probe spends no quota and provokes no errors.
- Sign-in screens: the account is signed in, and signing out would break the
  developer's login.

## Config touched

No trust entry was written on 2026-10-12: the scratch-folder trust box was
answered `q`, and the ready screen comes from the already-trusted main
checkout. The empty per-folder project dir the trust-box start created under
`~/.cursor/projects/` was deleted (checked afterwards). `~/.cursor/cli-config.json`
holds auth material and was never opened. The run left one `worker.log` append
behind, which is the harness's own bookkeeping.

The #96 probe (same day, `-L probe96`) started in a child of the trusted
main checkout, so no trust box appeared and no trust entry was written (the
`~/.cursor/projects/` listing was identical before and after). Its chat
transcript folder was deleted; the four small turns stay in the owner's
Cursor history and usage. Raw pane captures were deleted after the redacted
fixtures above were written. `cli-config.json` and `auth.json` never opened.
