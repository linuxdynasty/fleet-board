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
- **Secrets on screen** stay on the hub, behind the `LOGS` scope (owner by default; [0010](../adr/0010-session-logs.md)).
- **Emails** hidden by default; account keys are pseudonymous.

**Known limit, stated plainly.** The harness and the agent run as the same
user, as the harness does today, so a goal that goes wrong can do whatever that
user can do, including editing fleet-board's own files. That is the same
exposure the developer already accepts when running a harness unattended.
Developers who want more can use harness sandbox modes, rootless containers on
Linux, a root-owned install or a separate worker account with tmux
`server-access` (3.3+); none of it is required or assumed, and no hardening
guide is written yet (design.md, "Later, not scheduled"). One open case is decided by the owner before v1 dispatch is switched on
(ADR 0024, decision 20): in `local_trusted` mode a goal on the hub machine is
the owner to the loopback API ([02f](02f-hub-authentication.md#modes)).

**Checked 2026-10-01 (for a future hardening guide, not requirements):**
`codex sandbox -- <cmd>` on macOS runs under Seatbelt: all writes and all
network blocked by default, `~/.ssh` readable; forcing goals into it would
break `git push` and `gh`. Muse's Auto-review mode allowed a network request to
`api.github.com` on its own during a goal. tmux `server-access` is 3.3+.
