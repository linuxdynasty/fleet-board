# Folder-trust fixtures

Real first screens that decide whether a harness is waiting on a folder-trust
question, for plan item 0.9 (issue #54). The probe record and the answers are in
[01b](../specs/01b-harness-commands.md#folder-trust-probe-2026-10-10); the
owner steps and the `fleet-agent doctor` wording are in
[01 C3](../specs/01-agent-capabilities.md).

| Field | Value |
|---|---|
| Captured | 2026-10-10, macOS, tmux 3.7c, panes 200x50; two trust screens re-captured 2026-10-12 (tmux 3.8) for the auto-updated versions |
| Versions | Claude Code 2.1.296, Codex 0.160.0 and 0.162.1, Muse 1.4.4 and 1.4.6, Cursor `cursor-agent` 2026.10.01-e373342, OpenCode 1.18.34 |
| Method | `fleet-live-probe`: a throwaway tmux server (`-L probe54 -f /dev/null`), an empty scratch folder, the harness started by typing its name, `capture-pane -p` (ANSI stripped) read before every key |
| Quota | none: no prompt was sent to any model |

Files (under `internal/harness/<name>/testdata/<version>/`). Claude Code's
`trust-folder.txt` is byte for byte the file the usage-fixture set of issue #25
holds at the same path (captured again here so this change stands alone).

| File | Screen | Reached by |
|---|---|---|
| `cursor/.../trust-workspace.txt` | "Workspace Trust Required" box, `[a]` selected | start in a new plain folder |
| `cursor/.../ready-trusted-child.txt` | ready screen, no box | start in a new child of a trusted repo |
| `codex/.../trust-folder-git.txt` | "Folder access", "1. Trust and continue" selected | start in a new git repo |
| `codex/.../startup-daemon-settings-incompatible.txt` | "Background server has incompatible feature settings", "3. Cancel" selected | any start while a Codex background server runs |
| `codex/.../startup-update-prompt.txt` | "Update available", "1. Update now" selected | any start while an update exists |
| `muse/.../trust-workspace-plain-folder.txt` | "Do you trust this workspace?" | start in a new plain folder |
| `muse/.../trust-workspace-git-root.txt` | the same with a "Trust target:" line | start in a new folder inside a git repo |
| `muse/.../trust-workspace.txt` | older capture of the plain-folder screen, with a leading "Muse Code updated" line; kept as is, use `trust-workspace-plain-folder.txt` as the canonical one | start in a new plain folder |
| `claude/.../trust-folder.txt` | "Accessing workspace" box, "No, exit" selected | start in a new plain folder |
| `claude/.../ready-trusted-child.txt` | ready screen, no box | start in a new child of a trusted repo |
| `opencode/.../ready-new-folder.txt` | ready screen; OpenCode has no trust screen | start in a new git repo |
| `codex/0.162.1/trust-folder-git.txt` | "Folder access"; the second choice is now "2. Back to Agent Command Center" (was "2. Quit") | start in a new git repo, 2026-10-12 (issue #180) |
| `muse/1.4.6/trust-workspace.txt` | "Do you trust this workspace?", same two choices | start in a new plain folder, 2026-10-12 (issue #180) |

A parser tests the trust screens as a positive match and the `ready-*` files as
"not a trust screen". The Codex daemon and update prompts are separate startup
prompts (Triage), not trust screens.

## Redactions

- Paths are `~/scratch`, `~/work/project` and `~/work/project/sub`. The shell
  prompt line with the machine name was removed. Column positions are kept
  (spaces adjusted); trailing blank lines are trimmed.
- Nothing else in these screens is personal. Model names, the plan name and the
  banner text on the ready screens are as printed.

## Not captured

- The answers (a trusted-by-hand result) as screens: the "yes" answers write to
  the owner's harness config, so only the Cursor throwaway trust was answered "yes",
  and the entries it made were deleted.
- Claude Code's trust box inside a git repo: every git folder tried was already
  covered by a trusted repo, so only the plain-folder box was seen.

## Clean-up

The probe's tmux server was killed. Scratch folders (under the system temp
folder, the home folder and the repos folder) were removed. The Cursor trust
entries the probe made were deleted. Raw captures were deleted after the redacted
files were written.

## Re-verification, 2026-10-19 (issue #54)

Environment: macOS 27.0.1, tmux 3.8, default pane size (per-pane geometry not recorded).

Read-only re-run of the first screens on the installed versions, same
`fleet-live-probe` shape: throwaway servers (`-L probe54c-<harness> -f
/dev/null`), empty scratch folders, `capture-pane -p` read before any key, no
prompt sent to any model (no quota). Every trust screen got the "no" answer
(Esc, or Ctrl-C for Cursor without touching the box); nothing was trusted and
no harness config was edited.

| Harness and version | Observed | Against the fixture |
|---|---|---|
| Claude Code 2.1.296 | "Accessing workspace" box, "No, exit" selected, Esc cancels | same shape as `trust-folder.txt` |
| Codex 0.163.0 (newer than the fixtures) | "Folder access" / "Trust this folder?", "1. Trust and continue" selected, second choice "2. Back to Agent Command Center"; no daemon or update prompt; Esc backs out to the Agent Command Center | same shape as the 0.162.1 `trust-folder-git.txt`; not a new fixture (a new version dir needs its golden-table row; claiming 0.163.0 is the adapter lane's call) |
| Muse 1.4.10 (newer than the fixtures) | "Do you trust this workspace?", "1 Trust and continue" / "2 Quit"; Esc quits to the shell | same shape as the 1.4.6 `trust-workspace.txt`; not a new fixture, same reason |
| Cursor `cursor-agent` 2026.10.01 | "Workspace Trust Required" box, `[a]` selected; quit with Ctrl-C, no prompt key pressed | same shape as `trust-workspace.txt` |
| OpenCode 1.19.3 (newer than the fixtures) | straight to the ready screen ("Ask anything…") plus the passive "/update to upgrade to OpenCode 2.0" line | still no trust screen, as `ready-new-folder.txt` says |

Cleanup: all five servers killed; scratch folders removed. Starting
Cursor created one empty workspace folder under `~/.cursor/projects/`; it was
removed and the listing matches the baseline. `~/.codex/config.toml` and
Muse's `trust.json`/`settings.json` kept their baselined checksums.
`~/.claude.json` (checksummed, never opened) changed under the Claude start's
own bookkeeping — the same class as the Codex counter bump in 01b. It was left
as is, and restoration is now impossible: the baselined scratch copies were
deleted during cleanup, and overwriting the owner's live state file blind (its
contents were never read) would risk owner state for no verified gain. Its
mtime is unchanged since the probe window, so nothing newer was disturbed.
OpenCode touched only its own run state (`opencode.db`/log), left as is.
No auth file was opened; raw captures were deleted.

Still open after the 2026-10-19 run, unchanged: the Claude Code
plain-parent cell and the Codex YOLO-off plain-folder cell. Both need an owner
hand act (a yes-answer on a trust screen, or a `config.toml` edit), which the
agent never does (01 C3).

## Re-verification, 2026-10-25 (issue #54)

Environment: macOS 27.0.1, tmux 3.8, panes 200x50.

Read-only first screens on the installed versions, same `fleet-live-probe`
shape: throwaway servers (`-L probe54d-<harness> -f /dev/null`), empty scratch
folders (a plain folder; a fresh git repo for Codex and OpenCode),
`capture-pane -p` read before every key, no prompt sent to any model (no
quota). Every trust screen got the documented "no" answer (the Esc key; `q`
then Ctrl-C for Cursor — neither dismissed the box, so its server was killed
instead). Nothing was trusted, and no config file was edited by hand.

| Harness and version | Observed | Against the fixture |
|---|---|---|
| Claude Code 2.1.310 (newer than the fixture's 2.1.296) | "Accessing workspace" box, "No, exit" selected; Esc exits to the shell | same shape as `trust-folder.txt`; not a new fixture (a new version dir needs its golden-table row; claiming a version is the adapter lane's call) |
| Codex 0.163.0 | update prompt first ("0.163.0 → 0.164.0", Esc skips it; `codex --version` afterwards still reads 0.163.0, so no upgrade ran), then "Folder access" / "Trust this folder?", "1. Trust and continue" selected; Esc backs out to the Agent Command Center | same shape as the 0.163.0 `trust-folder-git.txt` |
| Muse 1.4.13 (newer than the fixture's 1.4.11) | **New shape.** The TUI brands as "Copilot v1.0.100"; the box reads "Confirm folder trust" / "Do you trust the files in this folder?" with "1. Yes" / "2. Yes, and remember this folder for future sessions" / "3. No (Esc)", "1. Yes" selected; Esc quits to the shell | differs from the 1.4.x `trust-workspace*.txt` fixtures ("Do you trust this workspace?" / "1 Trust and continue"). The 01 C3 doctor line for Muse still names the old screen; updating that wording and adding a 1.4.13 fixture row is the adapter lane's call — flagged here, not done here |
| Cursor `cursor-agent` 2026.10.01 | "Workspace Trust Required" box, "[a] Trust this workspace" selected; neither `q` nor Ctrl-C dismissed the box, so no key reached the trust option (no Enter was ever pressed) and the server was killed | same shape as `trust-workspace.txt` |
| OpenCode 1.19.3 | straight to the ready screen ("Ask anything…") plus the passive "/update to install v1.19.4" line | still no trust screen, as `ready-new-folder.txt` says |

Cleanup: all five servers killed (no `probe54d-*` socket remains); scratch
folders removed. Starting Cursor created one empty workspace folder under
`~/.cursor/projects/`; it was removed and the listing matches the baseline.
`~/.codex/config.toml` and Muse's `trust.json`/`settings.json` kept their
baselined checksums. `~/.claude.json` (checksummed, never opened) changed
under the Claude start's own bookkeeping — the same class as the 2026-10-19
note — and was left as is: restoring it blind (its contents were never read)
would risk owner state for no verified gain. No auth file was opened; raw
captures were deleted after the redacted notes above were written.

Still open, unchanged: the Claude Code plain-parent cell and the Codex
YOLO-off plain-folder cell. Both need an owner hand act (a yes-answer on a
trust screen, or a `config.toml` edit), which the agent never does (01 C3).
