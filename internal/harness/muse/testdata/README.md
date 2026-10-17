# Muse Code screen fixtures

Real screens from Muse Code, for the parser tests in `internal/harness/muse`
(plan item 0.1, issues #25 and #180). Files are in `1.4.4/`, `1.4.6/` and
`1.4.7/`.

| Field | Value |
|---|---|
| Harness version | `1.4.4/`: Muse Code 1.4.4 (1.4.4-R5419.1); `1.4.6/`: Muse Code 1.4.6 (1.4.6-R6000.1); `1.4.7/`: Muse Code 1.4.7 (1.4.7-R6101.1), from `muse --version` in the probe pane |
| Captured | 2026-10-10 (1.4.4) and 2026-10-12 (1.4.6, 1.4.7), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
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
| `goal-paused-by-interrupt.txt` | "goal paused by interrupt — resume with /goal resume" after the owner interrupts the goal (01 E12) | reconstructed, not a raw capture: the signal line is the probe-recorded string from the 2026-10-02 goal-control probe (01b); a raw capture replaces it when one lands |
| `provider-error-retrying.txt` | "HTTP 500 · retrying · attempt 6/10" when the provider returns a server error (01 E12) | reconstructed, not a raw capture: the signal line is the spec-recorded string from 01; a raw capture replaces it when one lands |

## 1.4.6 (2026-10-12, issue #180)

Same protocol (throwaway server `-L probe180`, killed at the end; every screen
read before the next key; no prompt sent, no quota spent). The binary had
auto-updated to 1.4.6 with no notice on screen. The trust screen was captured
in an empty scratch folder and answered "2 Quit" (Down and Enter, on purpose:
the default is "1 Trust and continue"). The other screens come from the home
folder, which `trust.json` already lists, so no trust entry was written.

| File | Screen | How reached |
|---|---|---|
| `1.4.6/trust-workspace.txt` | "Do you trust this workspace?", 2 choices, default 1 | start in a new folder |
| `1.4.6/startup-ready.txt` | version header, input box, footer (no "Model set to" line) | start in a trusted folder |
| `1.4.6/ready-ansi.txt` | the same screen with ANSI escapes | idle |
| `1.4.6/ready-input-has-draft.txt` | input box with unsent text | typed text |
| `1.4.6/ready-input-has-draft-ansi.txt` | the draft line with ANSI escapes | typed text, then Ctrl+U to clear |
| `1.4.6/usage.txt` | the "Session usage" box (Current 4%, Weekly 11%, real readings) | `/usage` (typed, then Enter on the read screen) |
| `1.4.6/status.txt` | the status box below the usage box | `/status` |
| `1.4.6/goal-paused-by-interrupt.txt` | "goal paused by interrupt — resume with /goal resume" after the owner interrupts the goal (01 E12) | reconstructed, not a raw capture: the signal line is the probe-recorded string from the 2026-10-02 goal-control probe (01b); a raw capture replaces it when one lands |
| `1.4.6/provider-error-retrying.txt` | "HTTP 500 · retrying · attempt 6/10" when the provider returns a server error (01 E12) | reconstructed, not a raw capture: the signal line is the spec-recorded string from 01; a raw capture replaces it when one lands |

What changed since 1.4.4: the ready screen has no "Model set to ..." line; the
status box is wider and adds `WORKSPACE` (`trusted · not found`, even though
no trust box appeared), `USAGE`, `CONTEXT` (`not projected · 1008K limit`),
`SESSION` (a UUID, zeroed here), `ACTIVITY` and `BILLING` rows, and a random
session nickname (`example-name` here). The model name, provider, login-kind
line and plan name are placeholders (`muse-model-x`, `example`,
`Example account`, `Muse Code Example Plan`); percentages and reset times are
the real ones. Replacements keep the box width (a longer placeholder drops a
pad space before the border).

Empty against draft: the empty input line carries no placeholder text at all,
only `❯` in blue (`38;2;90;160;255`); typed text renders in light grey
(`38;2;204;211;219`). A parser tells them apart by the presence and colour of
text after `❯`, not by dimming.

## 1.4.7 (2026-10-12, issue #25)

Same protocol (throwaway server `-L probe25 -f /dev/null`, killed at the end;
every screen read before the next key; no prompt sent, no quota spent). The
binary had auto-updated to 1.4.7 with no notice on screen. The trust screen
was captured in an empty scratch folder and answered "2 Quit" (Down and Enter,
on purpose: the default is "1 Trust and continue"). The other screens come
from the home folder, which `trust.json` already lists, so no trust entry was
written.

| File | Screen | How reached |
|---|---|---|
| `1.4.7/trust-workspace.txt` | "Do you trust this workspace?", 2 choices, default 1 | start in a new folder |
| `1.4.7/startup-ready.txt` | version header, input box, footer (no "Model set to" line) | start in a trusted folder |
| `1.4.7/ready-ansi.txt` | the same screen with ANSI escapes | idle |
| `1.4.7/ready-input-has-draft.txt` | input box with unsent text | typed text |
| `1.4.7/ready-input-has-draft-ansi.txt` | the draft line with ANSI escapes | typed text, then Ctrl+U to clear |
| `1.4.7/usage-command-typed.txt` | slash menu line `/usage  Show session usage` | typed `/usage` |
| `1.4.7/usage.txt` | the "Session usage" box (Current 17%, Weekly 5%, real readings) | Enter on the read screen |
| `1.4.7/status.txt` | the status box below the usage box | `/status` |
| `1.4.7/goal-paused-by-interrupt.txt` | "goal paused by interrupt — resume with /goal resume" after the owner interrupts the goal (01 E12) | reconstructed, not a raw capture: the signal line is the probe-recorded string from the 2026-10-02 goal-control probe (01b); a raw capture replaces it when one lands |
| `1.4.7/provider-error-retrying.txt` | "HTTP 500 · retrying · attempt 6/10" when the provider returns a server error (01 E12) | reconstructed, not a raw capture: the signal line is the spec-recorded string from 01; a raw capture replaces it when one lands |
| `1.4.7/help-general.txt` | `/help`, General tab (shortcuts), over the status box | `/help` |
| `1.4.7/help-commands-page-{first,middle,last}.txt` | `/help` Commands tab at the top, the middle and the end | Right, then Down |
| `1.4.7/help-commands-catalog.tsv` | all 49 built-in commands with their argument hints, `name<TAB>description` (derived by paging through four windows to cover every row) | see above |

What changed since 1.4.4/1.4.6: the catalog still holds 49 commands, but
`/goal` grew two verbs (`cancel|stop`: `[<objective>|edit
<objective>|clear|cancel|stop|pause|resume]`) and `/model` grew an argument
hint (`/model [<model-id>]`); nothing was added or removed. The ready, usage
and status screens match 1.4.6 otherwise (no "Model set to" line, same wide
status box, same empty-against-draft bytes). Percentages and reset times are
the real ones; names are placeholders as below.

No 1.4.6 command catalog was captured: the binary moved to 1.4.7 before the
catalog round, so the 1.4.6 Commands tab is skipped and 1.4.7 is the catalog
of record for the installed line.

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

- Error and retry screens other than the "HTTP 500 retrying" condition (now a
  reconstructed fixture, see above): they need a model call or a real failure,
  and the probe provokes neither.
- Sign-in and "Custom commands" screens (the account is signed in; the second
  lists installed skills), `/doctor`, `/model`, `/permissions`, `/settings`,
  `/logout`, `/upgrade`.
- A 1.4.2 capture (see above).

## Limits of these captures

The 1.4.4 captures are ANSI-stripped (`capture-pane -p`, no `-e`). The 1.4.6
and 1.4.7 `ready-ansi.txt` and `ready-input-has-draft-ansi.txt` keep the
escapes and prove the empty input line carries no placeholder text at all (see
the 1.4.6 section above; re-verified on 1.4.7).

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.config/muse/trust.json` and `~/.config/muse/settings.json` had the same
sha256 before and after (all captures; verified 2026-10-10, 2026-10-12 twice,
the second for issue #25).
`auth.json` was never opened in any capture (its mtime moved on 2026-10-12
because the harness itself runs, not because it was read).

1.4.4: `trust.json` was read once to find a trusted folder (it holds folder
paths and decisions only). The installed binary was replaced by Muse's own
auto-update.
1.4.6: `trust.json` was not opened; the home folder was already known to be
trusted. Nothing was installed or trusted.
