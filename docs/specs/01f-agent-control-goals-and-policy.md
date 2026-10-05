# 01f. Agent capabilities, part 2: control, goals, repositories, preflight, attach, logs, policy

- Status: Accepted (revised after an independent review)
- Date: 2026-09-29
- Part of: [01-agent-capabilities.md](01-agent-capabilities.md), split out to keep each file
  under the 500-line cap. 01 holds the principles, the capability map and sections A to E
  (identity, machine facts, harnesses, usage, tmux observation). This part holds sections F to
  M (tmux control, goals, repositories, preflight, terminal attach, session logs, policy and
  approval, self-management) and the closing sections: the pointer to
  [01e](01e-security-and-privacy.md), the hand-off to step 2, what was resolved and the open
  questions. Capability ids (F1, G2, M3 and so on) are unchanged.

## F. tmux control

All pass local policy, carry a `command_id`, and are idempotent except F3.

- **F1 Ensure session:** create a new session or attach to an existing one on
  the developer's tmux server (by name), in an allowed repo root, optionally
  starting a harness through its adapter; new sessions are tagged with
  `FLEET_SESSION_ID`, `FLEET_OWNER`, `FLEET_PANE_KEY` so they show as "started
  by fleet" while appearing in the developer's normal `tmux ls`.
- **Window naming.** Fleet-created windows are named after the harness and the
  work: default template `{harness} {work}`, where `{work}` is the work item's
  short form (`muse eshu#7398`, `codex eshu!12`, `claude PROJ-123`); for
  sessions without a work item, the name the developer typed. The template is
  configurable per machine or group. Fleet turns off `automatic-rename` for its
  own windows only; the developer's windows are untouched.
- **F2 Ensure window:** same, for a window in a session.
- **F3 Send keys:** literal text plus named keys, rate-limited, each with a
  purpose that decides the gate (02h). A prompt answer carries the `prompt_id`
  from E6; if that prompt is gone the agent refuses with `prompt_gone`. Not
  idempotent: a retry after an unknown outcome returns `UNKNOWN_OUTCOME`.
- **F4 Interrupt**, **F5 Respawn pane** (adapter `ResumeArgs`), **F6 Kill
  window.** Resize belongs to J1.
- **F8 Run harness command:** `RunHarnessCommand{target, command, args}`,
  where `command` must be in the machine's catalog (C4) and its class must be
  allowed for the requester. Only typed at an idle prompt (ready pattern
  matched), never into a running turn. Replaces free-typed slash commands
  through F3.

## G. Goals

- **G1 Goal hooks**: user-level hooks, inert unless the agent's goal
  registry names the pane; merge-installed with backup, verified, version and hash reported; Codex
  needs one-time `/hooks` trust; Muse takes them as a plugin package (probed). Repos that ship their own
  (eshu) are left alone. Design and probe record: [01d](01d-goal-hooks.md).
- **G2 Goal state** (v1): for fleet panes, and for panes where the agent finds
  an **observed goal** (a goal the owner started by hand; the rule is in
  [02h](02h-goal-binding-and-pane-signals.md#observed-goals-v1)). One event
  carrying the goal
  file's first line and `CONSENT:` line, the parsed state (working, `BLOCKED`
  with watcher and whether its pid is alive, `BLOCKED` with owner and action,
  `BLOCKED` with neither, `DONE`), and the worktree's HEAD commit and dirty
  flag. They travel together because the hub fingerprints them as one state.
  Goal text is agent-written and untrusted: length-capped, control characters
  stripped, rendered as data. For observed goals the agent reads eshu's
  `.claude/active-goal.<session_id>` files; the generic goal file of
  fleet-started goals is set by 01d (open question, #57).
- **G3 Start goal**, agent-side composite under one `command_id`, so it
  survives a hub outage: preflight → reuse an available clean pane working in a
  checkout of the goal's repo (new branch in place), or ensure a worktree (H2)
  and a new window → register the goal → wait for ready → model, effort, clear
  → write the goal file → type the `GoalCommand` → confirm the goal file. 14
  named steps, each a `command_id+step` with its own failure reason (02h).
- **G4 Goal control**: the harness's own `/goal` subcommands (pause,
  resume, edit, clear) typed at an idle prompt via F8, as the developer would
  ([01b](01b-harness-commands.md#goal-subcommands)); F4 Interrupt where a
  harness has no pause. Fleet-board defines no goal protocol of its own.
- **G5 Goal registry**: the agent's registry binds a goal to a pane (and its
  worktree and harness session id) for every harness. `StartGoal` and the hooks
  write the registry. G5 also reports the registry it rebuilds from
  `FLEET_GOAL_ID` (empty at first). A goal that the fleet did not start has no
  registry entry; attributing its goal file to a pane is E9 in
  [01](01-agent-capabilities.md).

## H. Repositories

- **H1 Repository inventory:** checkouts under allowed roots: path, repo
  reference (`RepoRef{host, path}`, path possibly nested like
  `group/subgroup/repo`, lowercase, no `.git`, same for SSH and HTTPS), default
  branch.
- **H2 Worktree ensure and prune**: create a worktree for a goal when the
  repo's own goal driver doesn't; prune it after the goal is verified merged
  and the grace period passes.
- **H3 Branch state:** per goal worktree, branch, HEAD, ahead/behind, dirty.
- **H4 Forge CLI status** (`gh` first, `glab` later; ADR 0012): signed in,
  user (token lines never logged), push access per repo normalized from the
  provider's permission (`gh` returned `ADMIN` for eshu-hq/eshu), cached 1 h; a change is `Event.forge_cli_status` (02i).

## I. Preflight

- **I1 Preflight** returns ok or every failed check with a reason code from
  the shared error enum (`harness_not_signed_in`, `push_access_missing`,
  `disk_low`, `agent_goal_cap_reached`, …; and for a pane the hub means to reuse,
  `session_workdir_mismatch`, `checkout_dirty`).

## J. Terminal attach

- **J1 Attach** from the browser to any session, existing or new. The browser
  terminal is a real tmux client (a PTY running `tmux attach`) on the
  developer's own server, so their prefix key and bindings work unchanged:
  `prefix n`/`p`/`1–9` to switch windows, `prefix o` and arrows to switch
  panes, splits, zoom. The board also shows a clickable window strip (index and
  name, like the status bar) for switching with the mouse. Browsers reserve a
  few shortcuts (for example Ctrl+W); the strip and the tmux prefix cover them.
- **Independent view.** The current window belongs to the tmux session, not the
  client, so the browser attaches through a **grouped session**
  (`tmux new-session -t <session> -s <view>`): same windows, its own current
  window, so switching in the browser never moves the developer's own terminal.
  The view session has `destroy-unattached on` and disappears when the browser
  disconnects (both confirmed in tmux's man page, 2026-10-01).
- Read-only mode drops input; resize; idle timeout; a banner on running goals;
  the plain `ssh -t <user>@<host> tmux attach -t <session>` line is shown for
  the developer's own terminal. Escape sequences that act on the viewer's
  machine (OSC 52 clipboard writes, hyperlinks) are stripped.

## K. Session logs

- **K1 Terminal log**: `tmux pipe-pane -o` for goal panes, and for the
  developer's own panes when turned on per session, into a local spool; shipped in compressed chunks with byte offsets; kept until
  acknowledged, up to a size cap; gaps are marked.
- **K2 Spool status and control**: pending bytes and gap marks in the
  snapshot and heartbeat; the hub can pause and resume shipping (for example
  when its disk is low).
- **K3 Harness transcripts** (v3): shipped incrementally by offset like K1,
  not at session end (goals run for days and `--continue` sessions don't end
  cleanly). Fleet panes only, opt-in per repo or machine.

## L. Policy and approval

- **L1 Local policy:** load and validate on start; report schema version and
  hash in the snapshot; emit an event if the file's hash changes while running.
  A changed policy takes effect only after an agent restart.
- **L2 Effective settings:** tighten-only merge of A8 under the local ceiling;
  report effective values, layers, refused and unknown fields (03e).
- **L3 Local approval**: desktop notification plus
  `fleet-agent approve|deny <command_id>` before the deadline.

## M. Self-management

- **M1** local audit log, **M2** OTEL, **M3** `doctor` (tmux, harness paths,
  mDNS, policy, cert expiry, hub reachability, clock skew, the probe folder's
  trust per harness (C3), and on Linux whether `loginctl enable-linger` is on).
- **M4 Drain and undrain**: from the hub or locally (`fleet-agent
  drain|undrain`); reported in snapshot and heartbeat. While draining, the agent
  refuses G3, harness-starting F1/F2 and F5 (except a pane with a registered live goal) with `draining`.
- **Upgrades are never a hub command** (a compromised hub could run new code
  everywhere). They run on the machine: `fleet-agent upgrade` fetches the next
  GitHub release, checks its checksum and swaps the binary (`fleetd upgrade`
  does the same for the hub); [02j](02j-hub-runtime.md#install-and-upgrade).

## Security and privacy

Moved to [01e-security-and-privacy.md](01e-security-and-privacy.md): what the hub
can make an agent do, credentials, untrusted text, the fleet scope, the known
limit.

## Open questions

None for this step (terminal log volume: measured on eshu-hq/eshu#7398, step 3).
