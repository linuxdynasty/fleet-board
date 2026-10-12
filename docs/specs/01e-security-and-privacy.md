# 1e. Security and privacy

- Status: Accepted
- Date: 2026-10-02
- Part of: [01-agent-capabilities.md](01-agent-capabilities.md)

Moved out of 01 so that file stays under the 500-line cap; the principles in 01
are unchanged.

fleet-board does not change how a machine is secured. The harness's own
settings (approval mode, sandbox, Auto-review, permission prompts) govern what
a goal may do, exactly as when the developer runs it by hand. What fleet-board
itself adds is kept safe by construction, with no setup:

- **What the hub can make an agent do** is limited to the typed commands in
  this spec (no shell strings, no raw exec), and never includes deny-class
  harness commands (C4) such as `/login`, `/logout`, `/permissions`, `/hooks`
  or buying usage.
- **Credentials are never touched** (principle 3).
- **Local policy** is a plain file in the developer's config directory with
  safe defaults. Its hash, and the hashes of the agent binary and goal hooks,
  are reported so the hub can show when they change; nothing blocks on this.
- **Untrusted text** (goal lines, pane summaries, prompts) is treated as data:
  length caps, control characters stripped, no HTML.
- **Fleet scope.** A **fleet pane** is a pane the fleet created (in a window it
  started, or in a session carrying `FLEET_SESSION_ID`) or one the agent's goal
  registry names. Pane content, logs and transcripts are read only for fleet
  panes unless the developer opts in. One exception, decided for v1 in
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (decision 1): the agent
  also looks for a goal file in the checkout of any pane that runs a known
  harness, so goals the owner starts by hand are shown as **observed goals**
  ([02h](02h-goal-binding-and-pane-signals.md#observed-goals-v1)). It reads only
  those goal files, only their first lines (G2), and never the pane's contents,
  logs or transcripts. Observed goals are shown and never controlled.
- **Harness configuration.** The agent never opens a harness's settings or
  trust files. Goal hooks add one narrow case: the agent may add,
  update and remove its own named hook entries in the user-level settings, never
  reads any other key, never logs file contents, and backs the file up first
  ([ADR 0024](../adr/0024-pre-build-owner-decisions.md), decision 3).
- **Developer checkouts.** In a checkout the agent writes only inside its own
  folder `.fleet-board/` at the top of the checkout a goal is starting in (or
  the goal's scratch folder), creates every file there itself, and never edits
  a file outside it: not `.git/info/exclude`, not the repo's `.gitignore`, not
  `.git/config`, nothing under `.git/`. The folder ignores itself, so
  `git status` shows nothing; `ReleaseGoal` removes it. How the writes are made
  safe (no symlinks followed, a foreign folder left alone):
  [01d2](01d2-goal-file-safe-writes.md).
- **Secrets on screen** stay on the hub, behind the `LOGS` scope (owner by default; [0010](../adr/0010-session-logs.md)).
- **Emails** hidden by default; account keys are pseudonymous.

**Known limit, stated plainly.** The harness and the agent run as the same
user, as the harness does today, so a goal that goes wrong can do whatever that
user can do, including editing fleet-board's own files. That is the same
exposure the developer already accepts when running a harness unattended.
Developers who want more can use harness sandbox modes, rootless containers on
Linux, a root-owned install or a separate worker account with tmux
`server-access` (3.3+); none of it is required or assumed, and no hardening
guide is written yet ([features.md](../features.md), the "later" rows). The
`local_trusted` case is decided ([ADR 0032](../adr/0032-local-trusted-approval-secret.md),
issue #62): in `local_trusted` mode a goal on the hub machine is still the owner
to the loopback API for everyday calls ([02f](02f-hub-authentication.md#modes)),
but approving a request and changing settings need the local approval secret,
which lives in the owner's dashboard session, not in a file a goal reads. Until
plan row 19 builds this, no goal starts.

**Checked 2026-10-01 (for a future hardening guide, not requirements):**
`codex sandbox -- <cmd>` on macOS runs under Seatbelt: all writes and all
network blocked by default, `~/.ssh` readable; forcing goals into it would
break `git push` and `gh`. Muse's Auto-review mode allowed a network request to
`api.github.com` on its own during a goal. tmux `server-access` is 3.3+.

## What the contract rules out

What the hub can never ask for, by construction of the contract:

- **No shell strings, no raw exec.** `Command` has no string that is run.
  `run_exec` is a reserved field number and name; `exec.raw` is a reserved
  capability. `SendKeys` carries literal text and `NamedKey`, never a tmux
  command string. `EnsureSession`/`EnsureWindow` name a harness; the agent
  resolves the path and launch args locally.
- **No paths from the hub.** `Workdir{repo, relative_path}`; the agent joins
  to an allowed root and rejects `..` (`REPO_ROOT_NOT_ALLOWED`).
- **No file reads.** There is no file RPC. The only text that leaves a
  machine is pane content (`CapturePane`, summaries, prompts, usage screens),
  goal-file lines, git/gh status lines and terminal logs, all caps applied.
- **No credentials.** `AccountIdentity` is read from status screens; `gh`
  token lines are dropped; `EnrollResponse.account_salt` is the only secret
  the hub sends, and it is never logged. Emails are empty unless
  `share_identity_email` is on.
- **Deny-class harness commands are never typed.** `RunHarnessCommand` is
  refused when the command is not in the catalog, the catalog hash is stale,
  the class is `DENY`, or the class exceeds the requester's role or local
  policy. Unknown commands are `DENY`.
- **No config push, no upgrade.** Hub `Settings` can only tighten; the agent
  reports what the local ceiling capped. There is no upgrade command.
- **No cross-machine addressing.** Every message is bound to the
  certificate's `machine_id`; `Terminal` and `SessionLog` streams are
  checked against it.
- **Hub-asserted role is defense in depth only.** A compromised hub can
  claim `ROLE_OWNER`; the real ceiling is the local policy's command classes
  and approval rules. The role x command matrix the agent applies, and the
  role the hub derives for a narrowed device or token, are in 02g and 02d.
