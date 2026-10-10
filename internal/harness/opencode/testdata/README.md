# OpenCode screen fixtures

Real output and screens from OpenCode, for the parser tests in
`internal/harness/opencode` (plan item 0.1, issue #25). Files are in `1.18.34/`.

| Field | Value |
|---|---|
| Harness version | OpenCode 1.18.34 (`opencode --version`; 01b was probed on 1.18.30) |
| Captured | 2026-10-10, macOS, tmux 3.7c, pane 200x50, zsh |
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

All captures are ANSI-stripped (`capture-pane -p`, no `-e`). The input placeholder `Ask anything… "Fix broken tests"` has no colour
in the text, so a typed draft that equals it cannot be told apart. `-e` variants were not captured.

Raw pane captures, the baseline copies of config files and the helper scripts
were deleted after the redacted files were written; only the files in this
folder remain.

## Config touched

`~/.config/opencode/opencode.jsonc` had the same sha256 before and after.
OpenCode's own `auth.json` path is printed by `providers list`; the file was
never opened.
