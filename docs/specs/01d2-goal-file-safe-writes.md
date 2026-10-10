# 01d2. Goal file: safe writes, cleanup and the self-ignoring check

Part of [01d](01d-goal-hooks.md). 01d says where the goal file lives
(`<checkout top>/.fleet-board/goal-<goal_id>`) and why. This file says exactly
how the agent writes, reads and removes it, so that a developer's checkout is
never harmed. Ruling on #57 (2026-10-10, binding). The rule it serves is in
[01e](01e-security-and-privacy.md) ("Developer checkouts"): the agent writes
only inside its own folder `.fleet-board/`, creates every file there itself, and
never edits a file outside it. Nothing under `.git/`, not `.git/info/exclude`,
not the repo's `.gitignore`, not `.git/config`.

## Safe writes

The folder sits in a place other programs (and the model) can also write to, so
the agent never trusts a name it did not just create. Everything below is done
through one open directory handle, with no path walked twice.

1. **Open the work folder once** as a directory handle (`workdir_fd`). The
   work folder is the pane's folder, already resolved to its real path (see
   [02h](02h-goal-binding-and-pane-signals.md#startgoal-steps), steps 4 and 5).
2. **The folder.** `mkdir(".fleet-board", 0700)` relative to `workdir_fd`. If it
   already exists, open it with `openat(workdir_fd, ".fleet-board",
   O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC)`. A symlink, a plain file or any folder
   that cannot be opened this way is `GOAL_FILE_WRITE_FAILED` with metadata
   `{"why":"goal_dir_not_plain"}`. The agent changes nothing, and Triage names
   the path (never any contents).
3. **The self-ignoring file.** `.fleet-board/.gitignore` is created with
   `O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW` through the folder handle, mode 0600,
   and holds exactly these bytes (they are also the ownership marker):

   ```
   # fleet-board goal files; this folder ignores itself
   *
   ```

   (two lines, each ending in a newline). If the file already exists the agent
   opens it `O_RDONLY|O_NOFOLLOW`, and requires: a regular file, link count 1,
   size at most 1 KiB, and bytes equal to the marker. Anything else is
   `GOAL_FILE_WRITE_FAILED` with `{"why":"goal_dir_foreign"}`. The agent never
   rewrites a `.gitignore` it did not create.
4. **The goal file.** `goal-<goal_id>` is created with
   `O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW` through the folder handle, mode 0600.
   A repeated step 12 (it is declarative) opens the name `O_NOFOLLOW`, checks
   a regular file with link count 1, and compares the content. A rewrite (the
   reopen path of [02h](02h-goal-binding-and-pane-signals.md#startgoal-steps),
   re-arm) writes a temporary file in the same folder through the handle and
   renames it over the name (`renameat`).
5. **Every read** (G2, `confirm_goal_file`, the 60 s rescan) is
   `openat(dir_fd, name, O_RDONLY|O_NOFOLLOW|O_CLOEXEC)`, then `fstat`: a
   regular file, link count 1, at most 4 KiB ([03k](03k-observed-goals.md)).
   Only the first line and the `CONSENT:` line are read, with control
   characters stripped. The agent already reads its own status file this way
   (`internal/agent/statusfile/read.go`): extend that pattern, do not write a
   second one. A confinement that follows links inside the root (Go's `os.Root`
   alone) is not enough.
6. **Hook scripts** treat the file as untrusted text. They refuse a file that
   fails `[ -f ]` or passes `[ -L ]`, read at most the first line, and send the
   harness only fixed words (the `DONE` or `BLOCKED` verdict), never the
   file's text.
7. **No git call, no `.git` read.** The agent does not look for the git
   directory at all: it runs no git command for this, reads nothing under
   `.git/` and honors no `GIT_DIR`. The one git exposure that stays is the
   `dirty` check of H3 ([01e](01e-security-and-privacy.md)). "The checkout
   stays clean" is proved by a repo table test, not by a runtime git call:
   a plain repo, a linked worktree, and a repo whose `.gitignore` tries
   `!.fleet-board/`, each with `.fleet-board/` filled in the way step 12 makes
   it, must give an empty `git status --porcelain`.
8. Everything the agent creates is 0700 (folders) and 0600 (files) under the
   developer's own user. No permission trick is claimed: a process running as
   that user can still change them. This is about not harming the checkout,
   not about hiding from the developer's own processes.

## Cleanup

- `ReleaseGoal` unlinks `goal-<its own goal_id>` through the folder handle
  (`unlinkat`, no follow). It then removes `.fleet-board/` only when
  `.gitignore` is the single entry left: unlink the `.gitignore`, then
  `rmdir`. A folder that holds anything else stays.
- `PruneWorktree` removes an agent-made worktree whole. A scratch folder is
  removed whole ([02h](02h-goal-binding-and-pane-signals.md#startgoal-steps)).
- At agent start the same cleanup runs for every registry entry that already
  ended (a crash between the end of a goal and its cleanup).
- The agent never deletes a `goal-*` file whose goal id it does not know.
  `fleet-agent doctor` lists such leftovers under the allowed roots by path
  (never contents) with the `rm` line for the developer to run.
- A goal file that vanishes during a goal (`git clean -fdx` deletes ignored
  files) is not a goal failure. The hub and the registry hold the goal; the
  file is a working copy. G2 reports the file as missing, and the agent does
  not recreate it by itself.

## Self-ignoring folder check (2026-10-10)

Run by the arbiter on git 2.54.0 to decide the question "can a folder keep
itself out of `git status` without any edit to a file the agent does not own?"
Each case used a folder `.fleet-board/` holding a `.gitignore` of `*` and a
goal file. Three repos: a plain repo, a plain repo with the folder in a
subfolder, and a linked worktree. A root `.gitignore` tried to undo it with
`!.fleet-board/`.

| Command | Result in all three repos |
|---|---|
| `git status --porcelain` | empty |
| `git status --porcelain --ignored` | `!! .fleet-board/` |
| `git check-ignore -v` on a file in the folder | `.fleet-board/.gitignore:2:*` |
| `git add -A` then `git diff --cached --stat` | nothing staged |
| `git stash -u` | "no local changes to save" |
| `git clean -nd` | would remove nothing |
| `git clean -ndx` | would remove `.fleet-board/` |
| `git worktree remove ../wt` (linked worktree) | rc=0 |

Control: the same folder without its `.gitignore` showed `?? .fleet-board/`.
So the `.gitignore` inside the folder is what hides it, and it keeps working
when the repo's own `.gitignore` tries to un-ignore the folder.

The `codex sandbox` runs behind the Codex `.git` finding in 01d did not change
`~/.codex/config.toml`: its SHA-256 was identical before and after (the content
was never read).

What this proves: `git status`, `add -A`, `stash -u` and a plain `clean` leave
the folder alone, and `worktree remove` works. What it does not prove:
newer or older git versions (only 2.54.0 was run), and any tool that is not
git (an editor or a backup tool may still show the folder).
