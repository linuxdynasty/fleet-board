# 01. Agent capabilities

- Status: Draft for review (revised after a Claude Fable 5.1 review)
- Date: 2026-09-29
- Inputs: [design.md](../design.md), ADRs [0004](../adr/0004-agent-connectivity.md),
  [0006](../adr/0006-tmux-sessions.md), [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md)

This spec lists everything `fleet-agent` can observe and do on a machine. It is
the input to the contracts (step 2): each capability becomes a command, an
event, a snapshot field or a stream.

## Principles

0. **Pass-through: nothing the developer doesn't already do.** fleet-board is
   a fleet dashboard and dispatcher for developers running subscription
   harnesses across machines and projects. It runs each harness exactly as the
   developer already runs it: same user, same logins, same permission and
   sandbox settings, same repos. It requires no root, no extra accounts, no
   sandbox mode and no new security setup. Its goals are visibility across
   machines and keeping subscription quota from sitting idle.
1. **The agent reports facts; the hub decides.** The agent is the source of
   truth for its machine (tmux, goal files, harnesses, usage, resources). The
   hub owns desired state, assignments, settings and anything computed from
   them (free slots, pacing, stuck). GitHub owns issue and PR truth.
2. **Observe freely, act only through typed commands** that pass the local
   policy ([0004](../adr/0004-agent-connectivity.md)).
3. **Never touch credentials.** No harness auth files, keychain items, `gh`
   tokens or secret environment variables. Identity and sign-in state come only
   from each tool's own status output.
4. **Declarative where possible**, so a repeated command is harmless.
5. **Every capability is advertised** with a versioned capability string
   (`usage.probe.codex@1`), so the hub never sends what a machine can't run.
6. **Your tmux is the fleet's tmux.** The agent works with the developer's
   normal tmux server, the one `tmux ls` shows: it lists every session there,
   lets the developer attach to any of them or create new ones, and starts
   goals there too. Only the usage probes live on a hidden internal server
   (`tmux -L fleet-probe`). Terminal logs and transcripts are recorded for goal
   sessions by default and for the developer's own sessions only when turned on
   per session.
7. **Testable without a real fleet**: every capability behind an interface with
   a fake; tmux and screen parsing tested against real tmux and captured
   fixtures.

## Capability map

Kind: **S** snapshot field, **E** event, **C** command (hub → agent),
**H** hub → agent message that is not a command, **T** stream, **L** local only.
Phase: **v1** read-only, **v2** actions, **v3** later.

| ID | Capability | Kind | Phase |
|---|---|---|---|
| A1 | Enroll | L → hub RPC | v1 |
| A2 | Find the hub (cached, mDNS, configured) | L | v1 |
| A3 | Connect, snapshot, reconcile; answer `SnapshotRequest` | S, H | v1 |
| A4 | Renew certificate | L → hub RPC | v1 |
| A5 | Heartbeat (raw counts, clock) | E | v1 |
| A6 | Detect wake and network change | L | v1 |
| A7 | Shutdown notice (`Bye` from agent) | E | v1 |
| A8 | Receive hub settings, acknowledge effective values | H, E | v1 |
| B1 | Machine facts (incl. timezone) | S | v1 |
| B2 | Resource samples | E | v1 |
| B3 | Power state | E | v2 |
| C1 | Harness inventory (+ parser and fixture versions) | S | v1 |
| C2 | Account identity and sign-in | S, E | v1 |
| C3 | Folder pre-trust at setup | L | v1 |
| C4 | Harness command catalog (discovered per machine) | S | v1 |
| D1 | Probe session: ensure, run, tear down, report health | C, E | v1 |
| D2 | Usage reading | E | v1 |
| D3 | Passive usage signals (banners, status line) | E | v1 |
| E1 | tmux inventory | S | v1 |
| E2 | Control-mode observer | L | v1 |
| E3 | Pane lifecycle (created, exited + exit status) | E | v1 |
| E4 | Activity transitions (active ↔ idle), not droppable | E | v1 |
| E5 | Pane summaries, droppable | E | v1 |
| E6 | Prompt detected (with `prompt_id`, source, confidence) | E | v1 |
| E7 | Harness ready (a level, with a snapshot field) | S, E | v2 |
| E8 | Harness hook events | E | v2 |
| E9 | Pane ↔ harness session mapping, goal registry | S, E | v1 |
| E10 | Capture pane | C | v1 |
| E11 | Pane availability (idle and available for work) | S, E | v1 |
| E12 | Harness condition (provider error, rate limit, sign-in, goal interrupted) | E | v1 |
| E13 | Developer input on a pane | S, E | v1 |
| F1 | Ensure session (create or reuse, optional harness) | C | v2 |
| F2 | Ensure window | C | v2 |
| F3 | Send keys (bound to `prompt_id` when answering) | C | v2 |
| F4 | Interrupt | C | v2 |
| F5 | Respawn pane (resume harness) | C | v2 |
| F6 | Kill window | C | v2 |
| F8 | Run harness command (allow-listed slash command) | C | v2 |
| G1 | Goal hooks: install and verify (Claude first) | C, S | v2 |
| G2 | Goal state (line + HEAD + dirty, one event) | E | v1 |
| G3 | Start goal (agent-side composite) | C | v2 |
| G4 | Goal control through the harness's own `/goal` | C | v2 |
| H1 | Repository inventory | S | v1 |
| H2 | Worktree ensure and prune | C | v2 |
| H3 | Branch state per goal worktree | S, E | v1 |
| H4 | GitHub CLI sign-in and push access | S, E | v1 |
| I1 | Preflight | C | v2 |
| J1 | Terminal attach to any session, existing or new (incl. resize) | T | v2 |
| K1 | Terminal log capture and shipping | T | v2 |
| K2 | Log spool status; pause and resume shipping | S, C | v2 |
| K3 | Harness transcripts, shipped incrementally | T | v3 |
| L1 | Local policy, with change detection | L, S, E | v1 |
| L2 | Effective settings (tighten-only merge) | S | v1 |
| L3 | Local approval | L, E | v2 |
| M1 | Local audit log | L | v1 |
| M2 | Telemetry (OTEL) | L | v1 |
| M3 | Doctor | L | v1 |
| M4 | Drain and undrain | C, L, S | v2 |

Not in v1 or v2: Tailscale lookup (v3), raw exec (capability string reserved,
not in the v1 contract), moving a goal between machines (v2 ships "stop here,
dispatch there").

## A. Identity, connection, settings

- **A1 Enroll.** `fleet-agent enroll --token T [--hub addr]`: verifies the hub
  against the CA fingerprint in the token (02g), sends CSR and B1 facts, pins
  the CA. Single-use, retry-safe. Hub returns the **account salt** (C2).
- **A2 Find the hub.** Cached last-good address, then mDNS (`_fleetd._tcp`),
  then configured addresses. Accept a candidate only if TLS verifies against
  the pinned CA. Linux without `avahi` skips mDNS; `doctor` says so.
- **A3 Connect and reconcile.** `Hello`, then a full snapshot, plus results
  the hub never acknowledged. The hub may send `SnapshotRequest` at any time.
- **A4 Renew certificate** at two-thirds of its lifetime.
- **A5 Heartbeat** every 15 s: load, raw counts (fleet panes, panes by harness,
  goal panes by goal state), wall clock and monotonic uptime for skew
  detection. The hub computes slots; the agent computes two aggregate goal counts
  for its own safety cap (02h).
- **A6 Wake and network change** triggers an immediate reconnect. Exact macOS
  and Linux APIs to be confirmed in a spike; fallback is a wall-clock jump
  check.
- **A7 Shutdown notice.** On SIGTERM (container stop, logout) the agent sends
  `Bye{reason}` so the hub marks it offline at once instead of after 3 minutes.
- **A8 Hub settings.** The hub sends machine and group settings with a
  **generation number**; the agent merges them under the local ceiling (L2) and
  acknowledges with the generation and the effective values. Labels are
  **assigned on the hub** and delivered here; local config can add labels but
  cannot remove a hub-assigned one, so a machine can't leave a stricter group
  by editing a file.

## B. Machine facts and resources

- **B1 Machine facts:** hostname, OS and version, architecture, CPU count,
  RAM, **IANA timezone and UTC offset**, agent version, tmux version, policy
  schema version.
- **B2 Resource samples** every 30 s: load, memory pressure, free bytes under
  each repo root and the state directory.
- **B3 Power state** (v2): AC or battery, battery %. macOS: `pmset -g batt`
  (checked). Linux: `/sys/class/power_supply/*`.

## C. Harnesses

A **harness adapter** is the extension point for a new tool:

| Method | Purpose |
|---|---|
| `Detect()` | Absolute path via login shell, version |
| `LaunchArgs`, `ResumeArgs` | Start interactive; resume (`claude --continue`, `codex resume --last`, `muse resume --last`) |
| `ReadyPattern`, `PromptPatterns`, `TrustPrompt` | Screen recognition |
| `StatusCommand`, `UsageCommand`, `ParseStatus`, `ParseUsage` | Identity and usage screens; versioned parsers |
| `GoalCommand(consent, text)` | The `/goal …` line |
| `CommandCatalog()` | Discover the slash commands this install offers, and classify them |
| `SessionIDSource` | How to learn the harness session id for a pane |
| `TranscriptLocator` | Where the harness keeps its transcript |
| `SupportedVersions` | Harness versions the parsers have fixtures for |

Harnesses in scope: Claude Code, Codex, Muse and **OpenCode** (installed on
all four of the author's machines, 1.18.30–1.18.34). OpenCode is a router
across several provider accounts with no `/usage` and no `/goal`; see
[01b-harness-commands.md](01b-harness-commands.md#opencode).

**Usage belongs to accounts, not harnesses.** A harness can draw on several
accounts (OpenCode: Anthropic, OpenAI, Google and Copilot by OAuth, DeepSeek
and Moonshot by API key), and several harnesses can share one account
(OpenCode on Anthropic OAuth spends the same Claude weekly limit as Claude
Code). Each session is linked to the account it is currently spending from,
and budgets are per account: a weekly % for subscriptions, a dollar cap per
period for API-key providers.

- **C1 Harness inventory:** name, version, path, parser version, and whether
  the version is within `SupportedVersions`. Outside it, usage readings are
  `unknown` and preflight refuses with `harness_version_unsupported`.
- **C2 Account identity and sign-in.** Read from the harness's own status
  screen, never from credential files. Checked 2026-09-29:
  - Claude Code `/status`: login method ("Claude Max account"), email,
    organization.
  - Codex `/status`: plan only ("Account: Pro").
  - Muse `/status`: name and email, billing.

  The agent sends an **account key** = hash(fleet salt from A1, identity it can
  see), so the same account matches across machines, plus a display label.
  **Emails are hidden by default**: the board shows the account's label and
  the agent sends only the key. An owner setting turns on sending and showing
  the email exactly as the harness displays it. This is pseudonymous, not anonymous:
  a compromised hub holding the salt could confirm a guessed email. Codex has no
  unique identity on screen (checked: `codex login status` prints only "Logged in
  using ChatGPT"), so the owner picks the Codex account for each machine on the
  hub (for example "Codex: ChatGPT Pro (personal)").
- **Machine naming.** Machines are named automatically from the hostname the
  agent reports at enrollment (B1); the owner can rename them. "Add machine" on
  the hub creates the enrollment token and a one-line install command. The owner
  may enter the expected hostname or IP first; the hub then warns if a
  different machine enrolls with that token. The hub still never connects to the
  machine ([0004](../adr/0004-agent-connectivity.md)). Identity is read by a short transient probe at agent
  start and when the harness version changes, so machines don't keep idle
  harness processes just for identity.
- **C3 Folder pre-trust.** At setup, the agent pre-trusts allowed repo roots so
  goals never stall on the trust prompt. It does not answer trust prompts at
  run time.

- **C4 Harness command catalog.** Each install offers many slash commands,
  and the set changes with the harness version and with the skills and plugins
  installed on that machine. The agent discovers the catalog in the probe
  session at start and on version change, reports it with a hash, and the
  adapter classifies every command (observe, session, goal, review,
  destructive, deny; unknown commands are denied). Observed lists and the class
  table: [01b-harness-commands.md](01b-harness-commands.md).

## D. Usage

- **D1 Probe session** (command from the hub, since the hub assigns one prober
  per account and fails over): `EnsureProbe{account_id, harness, interval}` and
  `StopProbe`. The agent keeps one idle harness session on the `fleet-probe`
  server, runs the usage command on schedule, and reports probe health (alive,
  last success, consecutive failures). No speed-up near the reserve line.
- **D2 Usage reading** (event). **The reading carries every detail the harness
  shows**, and the board displays them in the harness's own terms, so the owner
  sees exactly what they would see by typing the command. Observed fields:

  | Harness | Shown by the harness |
  |---|---|
  | Claude Code `/usage` | current session % used and reset; current week (all models) % used and reset; current week per model (for example Fable) % used and reset; the "what's contributing" breakdown, which Claude labels approximate and local to that machine |
  | Codex `/status` | weekly limit % left and reset; plan ("Account: Pro"); model and server; credits when shown (session logs carry `credits.has_credits` and `balance`) |
  | Muse `/usage` | current % used and reset; weekly % used and reset; the "as of" time; session tokens (input, cached, output, total), turns, subagents; plan ("Muse Code Power Usage") |

  Each reading has two parts:
  - **as shown:** every field the parser recognized, with the harness's labels,
    plus the raw screen text (personal data redacted per C2), for display;
  - **standardized:** a list of limit buckets (label, % used as integer 0–100
    or `unknown`, reset time in UTC, window length if known, and whether it is
    a short window or the weekly budget), for the scheduler. Codex's "% left"
    becomes "% used" here; the display keeps "% left".

  Also: `probe_id`, account key, `read_time`, parser version. Reset times on
  screen are local strings without a year ("Resets Sep 30 at 10pm"); the agent
  resolves them with B1's timezone and sends both the original text and UTC. On
  parse failure the reading is `unknown` and the redacted raw screen is still
  shown, never a guessed value.
- **Probe timing.** Every 10 minutes by default, configurable per account;
  plus an immediate probe when a goal on that account starts or finishes, and
  when a passive signal (D3) shows a jump.
- **D3 Passive usage signals.** Harnesses show usage without being asked:
  Claude's banner read "You've used 96% of your weekly limit · resets 10pm
  (America/New_York)" on 2026-09-30, and each harness has a configurable status
  line (`/statusline`). The adapter parses these from panes it is already
  observing and reports them as readings with `source: passive`. The scheduler
  prefers the latest probe reading and uses passive signals to notice sharp
  changes between probes.

## E. tmux observation

- **E1 Inventory:** every session, window and pane on the developer's tmux
  server (marked "yours" or "started by fleet" from `FLEET_*` tags), plus the
  internal `fleet-probe` server (internal sessions are marked and the hub
  ignores them). Panes carry their directory, repo and dirty flag when under an
  allowed root; attribute changes are `PaneChanged` events (02h). Windows are reported with their index and
  name exactly as the status bar shows them (developers name windows by
  purpose, e.g. `codex prompt 1`, `claude w2`, `btop`) and whether tmux
  renames them automatically. The harness in a pane is identified from the
  running process (`pane_current_command` and the adapter's `Detect()`), never
  from the window name. Panes are keyed by `(server, server_start_id, pane_id)` plus
  `FLEET_*` environment tags, because tmux ids restart from zero with a new
  server.
- **E2 Control-mode observer:** one `tmux -C` client per server, attached with
  the `ignore-size` client flag (added in tmux 3.2) so it never changes the
  size of the developer's windows, and with `refresh-client -f pause-after=<s>` (`%pause`, `%continue`,
  `%extended-output`; confirmed in tmux 3.7c's man page, confirm the release
  that introduced it is ≤ 3.2).
- **E3 Pane lifecycle:** created and exited with exit status. "Stopped without
  DONE" (the one-nudge rule) depends on this.
- **E4 Activity transitions:** active ↔ idle, with the time of last output.
  Never dropped under backpressure; the hub's stuck detection depends on it.
- **E5 Pane summaries:** last lines, ANSI stripped; at most one per pane every
  5 s; droppable.
- **E6 Prompt detected:** `prompt_id`, text, options, destructive flag, source
  (screen pattern or hook) and confidence. Send-to-pane stays disabled for a
  harness until its detection is verified (Claude first).
- **E7 Harness ready** (v2): whether the adapter's ready pattern is showing in
  a pane, as a snapshot field and an event for both edges.
- **E8 Harness hook events** (v2): turn end, session start, Codex interrupt,
  with the harness session id; per-harness events in [01d](01d-goal-hooks.md).
- **E9 Pane ↔ harness session mapping and the goal registry.** The agent's
  registry binds a goal to a pane (and its worktree and harness session id) for
  every harness; goal hooks record the session id against that entry (02h). This
  lets G2 attribute `.claude/active-goal.<session_id>` files to panes, and K3
  find transcripts.
- **E11 Pane availability.** Each harness pane reports whether it is **idle**:
  the adapter's ready pattern is showing, no goal is active in it (G2), and
  there has been no output or keyboard input for a configurable time (default 2
  minutes). Idle alone never makes a pane take work. A pane is **available for
  work** when:
  - fleet started it for a goal and that goal is verified merged, or
  - the developer marked it "available for work" on the board (their own
    sessions are never taken over without that mark).

  The agent reports changes as events and keeps the mark per pane (02h); the
  hub's dispatcher treats an available pane as free capacity, subject to pacing
  and machine limits, and starts the next goal in it (G3) instead of opening a
  new window. Any input from the developer clears "idle" at once (E13), and a
  goal is never typed into a pane whose input line is not empty.
- **E12 Harness condition:** provider errors, rate limits, sign-in needed (e.g.
  Muse's "HTTP 500 · retrying · attempt 6/10"), a goal interrupted or paused
  at the machine, reported until cleared.
- **E13 Developer input:** when a person last typed into a pane (snapshot field
  plus a rate-limited event); goal panes are never idle, so this is the signal.
- **E10 Capture pane:** visible screen or last N lines, ANSI stripped by
  default.

## F. tmux control (v2)

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

- **G1 Goal hooks** (v2): user-level hooks, inert unless the agent's goal
  registry names the pane; merge-installed with backup, verified, version and hash reported; Codex
  needs one-time `/hooks` trust; Muse not probed. Repos that ship their own
  (eshu) are left alone. Design and probe record: [01d](01d-goal-hooks.md).
- **G2 Goal state** (v1): for fleet panes only, one event carrying the goal
  file's first line and `CONSENT:` line, the parsed state (working, `BLOCKED`
  with watcher and whether its pid is alive, `BLOCKED` with owner and action,
  `BLOCKED` with neither, `DONE`), and the worktree's HEAD commit and dirty
  flag. They travel together because the hub fingerprints them as one state.
  Goal text is agent-written and untrusted: length-capped, control characters
  stripped, rendered as data.
- **G3 Start goal** (v2), agent-side composite under one `command_id`, so it
  survives a hub outage: preflight → reuse an available clean pane working in a
  checkout of the goal's repo (new branch in place), or ensure a worktree (H2)
  and a new window → register the goal → wait for ready → model, effort, clear
  → write the goal file → type the `GoalCommand` → confirm the goal file. 14
  named steps, each a `command_id+step` with its own failure reason (02h).
- **G4 Goal control** (v2): the harness's own `/goal` subcommands (pause,
  resume, edit, clear) typed at an idle prompt via F8, as the developer would
  ([01b](01b-harness-commands.md#goal-subcommands)); F4 Interrupt where a
  harness has no pause. Fleet-board defines no goal protocol of its own.

## H. Repositories

- **H1 Repository inventory:** checkouts under allowed roots: path, repo
  reference (`RepoRef{host, path}`, path possibly nested like
  `group/subgroup/repo`, lowercase, no `.git`, same for SSH and HTTPS), default
  branch.
- **H2 Worktree ensure and prune** (v2): create a worktree for a goal when the
  repo's own goal driver doesn't; prune it after the goal is verified merged
  and the grace period passes.
- **H3 Branch state:** per goal worktree, branch, HEAD, ahead/behind, dirty.
- **H4 Forge CLI status** (`gh` first, `glab` later; ADR 0012): signed in,
  user (token lines never logged), push access per repo normalized from the
  provider's permission (`gh` returned `ADMIN` for eshu-hq/eshu), cached 1 h; a change is `Event.forge_cli_status` (02i).

## I. Preflight

- **I1 Preflight** (v2) returns ok or every failed check with a reason code from
  the shared error enum (`harness_not_signed_in`, `push_access_missing`,
  `disk_low`, `agent_goal_cap_reached`, …; and for a pane the hub means to reuse,
  `session_workdir_mismatch`, `checkout_dirty`).

## J. Terminal attach (v2)

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

- **K1 Terminal log** (v2): `tmux pipe-pane -o` for goal panes, and for the
  developer's own panes when turned on per session, into a local spool; shipped in compressed chunks with byte offsets; kept until
  acknowledged, up to a size cap; gaps are marked.
- **K2 Spool status and control** (v2): pending bytes and gap marks in the
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
- **L3 Local approval** (v2): desktop notification plus
  `fleet-agent approve|deny <command_id>` before the deadline.

## M. Self-management

- **M1** local audit log, **M2** OTEL, **M3** `doctor` (tmux, harness paths,
  mDNS, policy, cert expiry, hub reachability, clock skew).
- **M4 Drain and undrain** (v2): from the hub or locally (`fleet-agent
  drain|undrain`); reported in snapshot and heartbeat. While draining, the agent
  refuses G3, harness-starting F1/F2 and F5 (except a pane with a registered live goal) with `draining`.
- **Upgrades are never a hub command** (a compromised hub could run new code
  everywhere); they run on the machine, through the package manager or script.

## Security and privacy

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
- **Fleet scope:** goal files, pane content, logs and transcripts are read only
  for fleet panes unless the developer opts in.
- **Secrets on screen** stay on the hub, behind the `LOGS` scope (owner by default; [0010](../adr/0010-session-logs.md)).
- **Emails** hidden by default; account keys are pseudonymous.

**Known limit, stated plainly.** The harness and the agent run as the same
user, as the harness does today, so a goal that goes wrong can do whatever that
user can do, including editing fleet-board's own files. That is the same
exposure the developer already accepts when running a harness unattended.
Developers who want more can follow an optional hardening guide (harness
sandbox modes, rootless containers on Linux, root-owned install, separate
worker account with tmux `server-access` on 3.3+); none of it is required or
assumed.

**Checked 2026-10-01 (for the hardening guide, not as requirements):**
`codex sandbox -- <cmd>` on macOS runs under Seatbelt with a default that
blocks all writes (even in the working directory) and all network, but allows
reading `~/.ssh`; forcing goals into it would break `git push` and `gh`. Muse's
Auto-review mode reviewed and allowed a network request to `api.github.com`
on its own during a goal. tmux `server-access` was added in 3.3.

## Hand-off to step 2

Contract-readiness rules, ADR changes, volumes and testing: [01c-handoff.md](01c-handoff.md).

## Resolved (2026-10-01)

- Codex account chosen per machine; machine names from hostname, renameable.
- Usage display mirrors each harness; probes every 10 min plus on events.
- Emails hidden by default; log volume measured before step 3.
- Security: pass-through, no root, no sandbox requirement, optional hardening guide.
- Auto-dispatch: subscriptions only (API keys manual); never OpenCode on Anthropic.

## Open questions

None for this step. Terminal log volume is being measured on a real Muse goal
(eshu-hq/eshu#7398, started 2026-10-01); results go into step 3.
