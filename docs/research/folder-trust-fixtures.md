# Folder-trust fixtures

Real first screens that decide whether a harness is waiting on a folder-trust
question, for plan item 0.9 (issue #54). The probe record and the answers are in
[01b](../specs/01b-harness-commands.md#folder-trust-probe-2026-10-10); the
owner steps and the `fleet-agent doctor` wording are in
[01 C3](../specs/01-agent-capabilities.md).

| Field | Value |
|---|---|
| Captured | 2026-10-10, macOS, tmux 3.7c, panes 200x50 |
| Versions | Claude Code 2.1.296, Codex 0.160.0, Muse 1.4.4, Cursor `cursor-agent` 2026.10.01-e373342, OpenCode 1.18.34 |
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
