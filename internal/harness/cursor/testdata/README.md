# Cursor screen fixtures

Real screens from `cursor-agent`, for the parser tests in
`internal/harness/cursor` (plan item 0.1, issues #25 and #180, row 30). Files
are in `2026.10.01/`.

| Field | Value |
|---|---|
| Harness version | `cursor-agent` 2026.10.01-e373342 (`cursor-agent --version`) |
| Captured | 2026-10-10 (`trust-workspace.txt`, `ready-trusted-child.txt`) and 2026-10-12 (`ready-ansi.txt`), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
| Plan | Pro+ (shown on `/usage`; see 01b) |

## How it was captured

Protocol: the `fleet-live-probe` skill. A throwaway tmux server (own socket in
a short temp folder, `-f /dev/null`), killed at the end. The agent was started
by typing `cursor-agent` in a shell pane, as the developer does. Every screen
was read with `capture-pane -p` before the next key. No prompt was sent to a
model, so no quota was spent.

Keys sent: `q` to leave the trust box (the "no" answer, on purpose; the
default is "[a] Trust this workspace"). Nothing was typed into a Cursor pane:
until plan item 0.19 proves how to tell leftover input from the fresh
placeholder, every `SendKeys` purpose answers `PANE_BUSY` on a Cursor pane
(02h), probe sessions included.

## Files

| File | Screen | How reached |
|---|---|---|
| `trust-workspace.txt` | "Workspace Trust Required" box, "[a] Trust this workspace" / "[q] Quit" | start in a new folder |
| `ready-trusted-child.txt` | ready screen, no box: "→ Plan, search, build anything", footer with model, approval mode and context share | start in a child of an already-trusted folder |
| `ready-ansi.txt` | the ready screen with ANSI escapes | start in the main checkout (already trusted), idle, 2026-10-12 |

## Redactions and clean-up

- Paths are `~/scratch`. The repo name on the ready footer is `a` and the
  branch is `main*`.
- Trailing spaces and trailing blank lines are trimmed.

## Placeholder finding (item 0.19 input)

`ready-ansi.txt` shows how the fresh placeholder renders: `→` dim, then `P`
in reverse video (`^[[0;7m`), then `lan, search, build anything` dim
(`^[[0;2m`). A typed draft was not captured (typing into a Cursor pane is
forbidden until 0.19 is proven), so the draft half of the comparison is still
open. A parser must not treat the placeholder text as typed input; the
attribute bytes above are the only proven tell so far.

## Not captured, and why

- A draft, permission prompt, or post-turn input line: typing into a Cursor
  pane is forbidden until 0.19 (02h).
- `/usage` with On-Demand on or off, `/goal` subcommands, pause and resume
  (0.21), account identity (0.22), probing cost (0.8): see the "Not probed"
  list in 01b; several need quota spend or docs.
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
