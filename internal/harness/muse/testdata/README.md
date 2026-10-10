# Muse Code screen fixtures

Real screens from Muse Code, for the parser tests in `internal/harness/muse`
(plan item 0.1, issue #25). Files are in `1.4.4/`.

| Field | Value |
|---|---|
| Harness version | Muse Code 1.4.4 (1.4.4-R5419.1), from `muse --version` in the probe pane |
| Captured | 2026-10-10, macOS, tmux 3.7c, pane 200x50, zsh |
| Plan | a subscription plan, named `Muse Code Example Plan` here (see redactions) |

**The binary auto-updated during the capture.** The first launch printed
"Muse Code updated 1.4.2-R4684.1 -> 1.4.4-R5419.1" before anything else, and
`--version` read 1.4.2 a few minutes earlier in the same run. There are no 1.4.2 fixtures;
the old binary is gone. 1.4.4 is the version the fixtures cover.

## How it was captured

Protocol: the `fleet-live-probe` skill. Throwaway tmux server (own socket in a
short temp folder, `-f /dev/null`), killed at the end. Muse was started by typing
`muse` in a shell pane in the developer's normal login. Each screen was read
with `capture-pane -p` before the next key. No prompt was sent, so no quota was
spent. The trust screen was captured in an empty scratch folder and answered
"2 Quit" (the default was "1 Trust and continue", so the answer was chosen on
purpose with Down and Enter). The other screens come from the home folder,
which `trust.json` already lists as trusted, so no trust entry was written.

Keys sent: the typed slash commands, Enter, Down (menu paging, and the trust
choice), Right (help tab), Escape, Ctrl+U (clear the input), and `/quit`.

## Files

| File | Screen | How reached |
|---|---|---|
| `trust-workspace.txt` | update line, "Do you trust this workspace?", 2 choices, default 1 | start in a new folder |
| `startup-ready.txt` | version header, "Model set to ...", data-use notice, input box, footer | start in a trusted folder |
| `usage-command-typed.txt` | slash menu line `/usage  Show session usage` | typed `/usage` |
| `usage.txt` | the "Session usage" box: tokens, turns, subagents, "Current" and "Weekly" percent used with reset times, "as of" | Enter |
| `status.txt` | the status box (model, workspace, access, account, session, billing) below the usage box | `/status` |
| `help-general.txt` | `/help`, General tab (shortcuts), over the status box | `/help` |
| `help-commands-page-{first,middle,last}.txt` | `/help` Commands tab, "Browse built-in commands", at the top, the middle and the end | Right, then Down |
| `help-commands-catalog.tsv` | all 49 built-in commands with their argument hints, `name<TAB>description` (derived by paging) | see above |
| `ready-input-has-draft.txt` | input box with unsent text | typed text |

## Redactions and clean-up

- Paths are `~` and `~/scratch`.
- Account name and email on the status box are `Example User (user@example.com)`;
  the random session nickname is `example-name`; session ids are
  `00000000-0000-4000-8000-00000000000N`.
- Identifying account strings were replaced with placeholders of about the same
  width: the model name (`muse-model-x`), the provider shown under it
  (`example`), the login-kind line (`Example account`) and the plan name
  (`Muse Code Example Plan`). The usage percentages and reset times are the
  real ones. A parser must not depend on these names.
- Replacements keep the box width. Trailing spaces and blank lines are trimmed;
  shell prompt and direnv lines are removed.
- Several screens keep the status or usage box from an earlier command above
  the input line, as the real pane showed it.

## Not captured, and why

- The "HTTP 500 retrying" condition and every other error or retry screen: they
  need a model call or a real failure, and the probe provokes neither.
- Sign-in and "Custom commands" screens (the account is signed in; the second
  lists installed skills), `/doctor`, `/model`, `/permissions`, `/settings`,
  `/logout`, `/upgrade`.
- A 1.4.2 capture (see above).

## Limits of these captures

All captures are ANSI-stripped (`capture-pane -p`, no `-e`). The empty input line (`❯`) and an input line with typed text differ by
text only; whether the empty line carries a dim placeholder, as on other
harnesses, was not seen. `-e` variants were not captured.

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.config/muse/trust.json` and `~/.config/muse/settings.json` had the same
sha256 before and after.
`trust.json` was read once to find a trusted folder (it holds folder paths and
decisions only). The installed binary was replaced by Muse's own auto-update.
`auth.json` was never opened.
