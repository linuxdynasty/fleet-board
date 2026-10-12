# OpenCode screen fixtures

Real output and screens from OpenCode, for the parser tests in
`internal/harness/opencode` (plan item 0.1, issues #25 and #180). Files are in
`1.18.34/` and `1.19.0/`.

| Field | Value |
|---|---|
| Harness version | `1.18.34/`: OpenCode 1.18.34; `1.19.0/`: OpenCode 1.19.0 (`opencode --version`; 01b was probed on 1.18.30) |
| Captured | 2026-10-10 (1.18.34) and 2026-10-12 (1.19.0), macOS, tmux 3.7c/3.8, pane 200x50, zsh |
| Providers | none signed in on this machine (see `providers-list.txt`) |

## How it was captured

Protocol: the `fleet-live-probe` skill. Throwaway tmux server (own socket in a
short temp folder, `-f /dev/null`), killed at the end. The CLI commands were typed
in a shell pane; the TUI was started by typing `opencode` in an empty scratch
folder. Each screen was read with `capture-pane -p` before the next key. No
prompt was sent and no model was called. The "Big Pickle OpenCode Zen" model
on the model line is OpenCode's own default; it was not changed.

Keys sent: the typed commands, Enter, Down (menu paging), Backspace, Escape,
and `/exit`.

## Files

| File | Output | How reached |
|---|---|---|
| `providers-list.txt` | `opencode providers list`: "Credentials", the path of the harness's own store, "0 credentials" | CLI |
| `stats.txt` | `opencode stats`: OVERVIEW (sessions, messages, days) and COST & TOKENS, all zero here | CLI |
| `cli-help.txt`, `providers-help.txt`, `stats-help.txt` | `--help` of the CLI, `providers` and `stats` | CLI |
| `tui-ready.txt` | start screen: logo, input box, model line "Build · Big Pickle OpenCode Zen", tip "Run /connect ...", status bar with version | `opencode` |
| `tui-slash-menu-first.txt`, `tui-slash-menu-last.txt` | the `/` menu at the top and at the end (10 rows each) | typed `/`, Down |
| `tui-slash-menu-catalog.tsv` | all 17 commands, `name<TAB>description` (derived by paging until the list wrapped) | see above |
| `tui-status-dialog.txt` | `/status`: "No MCP Servers", "No Formatters", "No Plugins" | `/status` |
| `tui-help-dialog.txt` | `/help`: points to ctrl+p | `/help` |

## 1.19.0 (2026-10-12, issue #180)

Same protocol (throwaway server `-L probe180`, killed at the end; every screen
read before the next key; no prompt sent and no model called). The TUI was
started by typing `opencode` in an empty scratch folder; the CLI outputs ran
in a fresh shell window after the TUI was left with Ctrl+C.

| File | Output | How reached |
|---|---|---|
| `1.19.0/tui-ready.txt` | start screen: logo, input box, model line "Build · Big Pickle OpenCode", "/update to upgrade to OpenCode 2.0" hint, status bar with version | `opencode` |
| `1.19.0/tui-ready-ansi.txt` | the same screen with ANSI escapes | idle |
| `1.19.0/tui-ready-input-has-draft.txt` | input box with unsent text | typed text |
| `1.19.0/tui-ready-input-has-draft-ansi.txt` | the draft line with ANSI escapes | typed text, then Ctrl+U to clear |
| `1.19.0/tui-status-dialog.txt` | `/status`: "No MCP Servers", "No Formatters", "No Plugins" | `/status` |
| `1.19.0/providers-list.txt` | `opencode providers list`: "Credentials", the harness's own store path, "0 credentials" | CLI |
| `1.19.0/stats.txt` | `opencode stats`: OVERVIEW, COST & TOKENS and TOOL USAGE, all real | CLI |

What changed since 1.18.34: the ready screen points at `/update to upgrade to
OpenCode 2.0` and the model line reads "Big Pickle OpenCode" (was "Big Pickle
OpenCode Zen"); a "Getting started ... Connect provider /connect" onboarding
panel appeared during the run (seen, not kept); `stats` now shows real totals
(1 session from this probe run: 29.5K avg tokens, one `bash` tool call), so a
parser sees nonzero numbers and the TOOL USAGE block for the first time. Still
no provider signed in, so the signed-in model line and `stats` with history
from other sessions stay uncaptured.

Placeholder against draft: the placeholder renders in grey
(`38;2;128;128;128`); typed text renders near-white (`38;2;238;238;238`). A
parser must match the colour, not the words.

## Redactions and clean-up

- Paths are `~/scratch`. The status bar keeps its right-aligned version.
- Trailing spaces and trailing blank lines are trimmed; leading blank lines in
  the TUI screens are kept (the logo is placed by row); shell prompt lines are
  removed from the CLI outputs.

## Not captured, and why

- `providers list` with a login, the model line with a signed-in provider, and
  `stats` with real totals: this machine has no provider and no sessions now
  (01b's 1.18.30 probe saw both). Needs a machine that is signed in.
- A startup, update, trust or sign-in screen: none appeared (no folder-trust
  prompt, as 01b says).
- A model-call screen of any kind, and `opencode models` output (public list,
  not kept).
- The `/variants` command and installed command packs: not in this menu now.

## Limits of these captures

The 1.18.34 captures are ANSI-stripped (`capture-pane -p`, no `-e`). The
1.19.0 `tui-ready-ansi.txt` and `tui-ready-input-has-draft-ansi.txt` keep the
escapes (see the 1.19.0 section above).

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.config/opencode/opencode.jsonc` had the same sha256 before and after
(both captures; verified 2026-10-10 and 2026-10-12).
OpenCode's own `auth.json` path is printed by `providers list`; the file was
never opened.
