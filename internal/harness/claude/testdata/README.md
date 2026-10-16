# Claude Code screen fixtures

Real screens from Claude Code, for the parser tests in `internal/harness/claude`
(plan item 0.1, issue #25). Files are in `2.1.296/`.

| Field | Value |
|---|---|
| Harness version | Claude Code 2.1.296 (`claude --version`, run in the probe pane before and after; unchanged) |
| Captured | 2026-10-10, macOS, tmux 3.7c, pane 200x50, zsh |
| Plan | Claude Max (shown on the screens) |

## How it was captured

Protocol: the `fleet-live-probe` skill. A throwaway tmux server (own socket
in a short temp folder, `-f /dev/null`), killed at the end. Claude was started by
typing `claude` in a shell pane, as the developer does, in the developer's normal
login. Every screen was read with `capture-pane -p` (ANSI stripped) before the
next key. No prompt was sent to a model, so no quota was spent. The trust
screen was captured in an empty scratch folder and answered "No, exit" (the
default, on purpose); the other screens come from a folder Claude already
trusted, so no trust entry was written.

Keys sent: the typed slash commands, Enter, Left and Right (tab switch), Down
(menu paging), Escape, Shift+Tab (permission mode cycle, back to the start
mode), and `/exit`.

2026-10-12 addition (issue #180): the two `-ansi.txt` files only. Same
protocol and version, ready screen in an already-trusted folder. Keys sent:
unsent draft text, Ctrl+U to clear it, and `/exit`. No prompt was sent, so no
quota was spent. `~/.claude/settings.json` had the same sha256 before and
after; `~/.claude.json` was never opened. The stale trust-box rows above the
banner in the raw capture (same pane, earlier start) were cut; each file
starts at the Claude banner.

## Files

| File | Screen | How reached |
|---|---|---|
| `passive-banner.txt`, `passive-banner-malformed.txt` | the weekly-limit usage banner (01 D3) and its half-read shape | spec-recorded, not a live capture: no probe has ever caught a near-limit banner on demand, so these hold the exact shape 01 D3 records (seen 2026-09-30) with the `Example/Zone` placeholder; a live capture replaces them when one lands, and passive stays out of the conformance table until then |
| `trust-folder.txt` | "Accessing workspace" trust box, default "No, exit" | start in a new folder |
| `startup-ready.txt` | banner, "Updated to latest" release line, MCP warning, ready prompt | start in a trusted folder |
| `ready-auto-mode.txt`, `ready-manual-mode.txt`, `ready-accept-edits-mode.txt`, `ready-plan-mode.txt` | the ready prompt in each permission mode (status line text differs) | Shift+Tab |
| `ready-input-has-draft.txt` | ready prompt with unsent text on the input line | typed text, then Escape twice to clear |
| `ready-ansi.txt` | the ready prompt and dim placeholder, with ANSI escapes (`capture-pane -e -p`) | idle on the ready screen, 2026-10-12 |
| `ready-input-has-draft-ansi.txt` | the draft input line, with ANSI escapes | typed text, then Ctrl+U to clear, 2026-10-12 |
| `usage-command-typed.txt` | the slash menu with `/usage` highlighted | typed `/usage` |
| `usage.txt` | the `/usage` dialog: session cost, plugin footprint, current session, weekly all-models, weekly Fable, contributors | Enter |
| `status.txt` | the Status tab: version, session, login method, organization, email, model, MCP counts | Left twice from the Usage tab |
| `help-general.txt` | `/help`, General tab | `/help` |
| `help-commands-page-{first,middle,last}.txt` | `/help` Commands tab, "Browse default commands", at the top, the middle and the end | Right, then Down |
| `help-commands-catalog.tsv` | all 107 default commands, `name<TAB>description` as the screen shows them (derived by paging) | see above |
| `release-notes.txt` | `/release-notes` version picker | `/release-notes`, then Escape (no version opened) |
| `exit.txt` | the screen after `/exit` ("Resume this session with") | `/exit` |

`usage.txt` and `status.txt` are taller than the pane, so they were cut from
the pane history, from the tab line to "Esc to cancel".

## Redactions and clean-up

- Home and project paths are `~`, `~/scratch` and `~/work/project`.
- Email, account name and organization are `user@example.com` (the
  organization line is the email plus "'s Organization", as Claude prints it).
- Session ids are `00000000-0000-4000-8000-00000000000N`; the messaging socket
  path is `uds:/tmp/cc-socks/0.sock`.
- Installed plugin names (`plugin-a`, `plugin-b`) and the skill and subagent
  names under "What's contributing" (`example-skill-*`, `example-agent-*`)
  are placeholders. Percentages and times are the real ones.
- The time zone name on the reset lines of `usage.txt` is `Example/Zone` (it
  was the developer's real zone). The reset text is otherwise real. The spec
  resolves reset times with the machine's time zone (01 and 03b), so a parser
  must not need the printed name to be a real zone; if a test must load it, swap
  in a real zone such as `Etc/UTC` in the test, not in the fixture.
- Replacements keep the column positions (spaces are adjusted). Trailing
  spaces and trailing blank lines are trimmed; shell prompt and direnv lines
  that came from the pane's shell are removed.

## Not captured, and why

- Permission and approval prompts, and limit or error screens: they need a
  model call or a real limit, and the probe spends no quota and provokes no errors.
- The sign-in screen: the account is signed in, and `/login` is a deny-class
  command (01b).
- Config tab, Stats tab, "Custom commands" tab: owner settings, activity
  numbers and installed skills; not input to any parser.
- `/context`, `/doctor`, `/mcp`, `/model`, `/permissions`, `/config`: list
  installed servers or skills, or are deny-class.

## Limits of these captures

Most captures are ANSI-stripped (`capture-pane -p`, no `-e`). The empty-input
placeholder therefore looks like typed text with no colour to tell them apart
in those files, and a draft that equals the placeholder text cannot be told
from the placeholder. A parser must not treat the placeholder as an
empty-input proof by text alone.

`ready-ansi.txt` and `ready-input-has-draft-ansi.txt` (2026-10-12, issue #180)
keep the escapes: the placeholder renders dim (`SGR 2`, `^[[2mTry ...^[[0m`)
while typed draft text renders in the default colour. The suggestion inside
the placeholder rotates (`Try "refactor view-session.js"` here), so a parser
must match the dim attribute, not the words. Escape twice did not clear the
draft on 2.1.296 (it did on the 2026-10-10 capture); Ctrl+U did.

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.claude/settings.json` had the same sha256 before and after.
`~/.claude.json` changed (size and time), as it does whenever any Claude session
runs; the developer's other live sessions write it too, so it cannot be restored
byte for byte and was not opened. Nothing was installed or trusted.
Credential files and the keychain were never opened.
