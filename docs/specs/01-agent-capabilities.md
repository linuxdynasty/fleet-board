# 01. Agent capabilities

- Status: Accepted (revised after an independent review)
- Date: 2026-09-29
- Inputs: ADRs [0004](../adr/0004-agent-connectivity.md),
  [0006](../adr/0006-tmux-sessions.md), [0009](../adr/0009-machine-groups-and-layered-settings.md),
  [0010](../adr/0010-session-logs.md)

This spec lists everything `fleet-agent` can observe and do on a machine. Sections A to E
are here; sections F to M (control, goals, repositories, preflight, attach, logs, policy,
self-management) and the closing sections are in
[01f-agent-control-goals-and-policy.md](01f-agent-control-goals-and-policy.md). It is
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
Phase: **v1** the whole local product (watching, dispatch, sessions, logs), **v3** remote. "Later" items are not scheduled (the rows marked `later` in [features.md](../features.md)). ADR 0025 retired the middle release.

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
| B3 | Power state | E | v1 |
| C1 | Harness inventory (+ parser and fixture versions) | S | v1 |
| C2 | Account identity and sign-in | S, E | v1 |
| C3 | Probe folder: one fleet-owned folder the owner trusts once by hand; doctor explains an untrusted one | L | v1 |
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
| E7 | Harness ready (a level, with a snapshot field) | S, E | v1 |
| E8 | Harness hook events | E | v1 |
| E9 | Pane ↔ harness session mapping | S, E | v1 |
| E10 | Capture pane | C | v1 |
| E11 | Pane availability (idle, and the developer's mark through the command `SetPaneAvailability`) | S, E, C | v1 |
| E12 | Harness condition (provider error, rate limit, sign-in, goal interrupted) | E | v1 |
| E13 | Developer input on a pane | S, E | v1 |
| F1 | Ensure session (create or reuse, optional harness) | C | v1 |
| F2 | Ensure window | C | v1 |
| F3 | Send keys (bound to `prompt_id` when answering) | C | v1 |
| F4 | Interrupt | C | v1 |
| F5 | Respawn pane (resume harness) | C | v1 |
| F6 | Kill window | C | v1 |
| F7 | Scratch directory for a goal with no repo: ensure, collect (`scratch`, ADR 0021) | C | v1 |
| F8 | Run harness command (allow-listed slash command) | C | v1 |
| G1 | Goal hooks: install and verify (Claude first) | C, S | v1 |
| G2 | Goal state (line + HEAD + dirty, one event) | E | v1 |
| G3 | Start goal (agent-side composite) | C | v1 |
| G4 | Goal control through the harness's own `/goal` | C | v1 |
| G5 | Goal registry: register, release, report | S, E, C | v1 |
| H1 | Repository inventory | S | v1 |
| H2 | Worktree ensure and prune | C | v1 |
| H3 | Branch state per goal worktree | S, E | v1 |
| H4 | GitHub CLI sign-in and push access | S, E | v1 |
| I1 | Preflight | C | v1 |
| J1 | Terminal attach to any session, existing or new (incl. resize) | T | v1 |
| K1 | Terminal log capture and shipping | T | v1 |
| K2 | Log spool status; pause and resume shipping | S, C | v1 |
| K3 | Harness transcripts, shipped incrementally | T | v3 |
| L1 | Local policy, with change detection | L, S, E | v1 |
| L2 | Effective settings (tighten-only merge) | S | v1 |
| L3 | Local approval | L, E | v1 |
| M1 | Local audit log | L | v1 |
| M2 | Telemetry (OTEL) | L | v1 |
| M3 | Doctor | L | v1 |
| M4 | Drain and undrain | C, L, S | v1 |

Not in v1: Tailscale lookup (v3), raw exec (string reserved, not in the
contract), moving a goal between machines (in v1 the owner stops the goal here
and dispatches it there).

## A. Identity, connection, settings

- **A1 Enroll.** `fleet-agent enroll --token T [--hub addr]`: verifies the hub
  against the CA fingerprint in the token (02g), sends CSR and B1 facts, pins
  the CA. Single-use, retry-safe. Hub returns the **account salt** (C2).
- **A2 Find the hub.** Cached last-good address, then mDNS (`_fleetd._tcp`),
  then configured addresses. Accept a candidate only if TLS verifies against
  the pinned CA. No multicast interface: mDNS is skipped; `doctor` says so.
  mDNS is best effort (ADR 0028): a host firewall that drops direct UDP
  replies, such as Linux `ufw` with "deny incoming", usually hides the hub, so
  the configured address is the path that does not depend on multicast.
  `doctor` reports an mDNS browse that found no hub and names that cause.
  `enroll` without `--hub` that finds no hub exits non-zero, names `--hub` and
  the likely firewall cause, and sends no token or CSR.
- **A3 Connect and reconcile.** `Hello`, then a full snapshot, plus results
  the hub never acknowledged. The hub may send `SnapshotRequest` at any time.
- **A4 Renew certificate** at two-thirds of its lifetime.
- **A5 Heartbeat** every 15 s: load, raw counts (fleet panes, panes by harness,
  goal panes by goal state), wall clock and monotonic uptime for skew
  detection. The hub computes slots; the agent computes two aggregate goal counts
  for its own safety cap (02h).
- **A6 Wake and network change** triggers a reconnect after a short random
  delay (03 A2). The macOS and Linux APIs are settled by spike 0.6 of the v1
  plan; the fallback is a wall-clock jump check.
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
- **B3 Power state**: AC or battery, battery %. macOS: `pmset -g batt`
  (checked). Linux: `/sys/class/power_supply/*`.

## C. Harnesses

A **harness adapter** is the extension point for a new tool:

| Method | Purpose |
|---|---|
| `Detect()` | Absolute path via login shell, version |
| `LaunchArgs`, `ResumeArgs` | Start interactive; resume (`claude --continue`, `codex resume --last`, `muse resume --last`, `cursor-agent --resume <chatId>`) |
| `ReadyPattern`, `PromptPatterns`, `TrustPrompt` | Screen recognition |
| `StatusCommand`, `UsageCommand`, `ParseStatus`, `ParseUsage` | Identity and usage screens; versioned parsers |
| `GoalCommand(consent, text)` | The `/goal …` line |
| `CommandCatalog()` | Discover the slash commands this install offers, and classify them |
| `SessionIDSource` | How to learn the harness session id for a pane |
| `TranscriptLocator` | Where the harness keeps its transcript |
| `SupportedVersions` | Harness versions the parsers have fixtures for |

Harnesses in scope: Claude Code, Codex, Muse, **Cursor** (`cursor-agent`, the
CLI; first-class from [ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md),
its adapter facts in [01b](01b-harness-commands.md#cursor)) and **OpenCode**
(installed on all four of the author's machines, 1.18.30–1.18.34). OpenCode is a router
across several provider accounts with no `/usage` and no `/goal`; see
[01b-harness-commands.md](01b-harness-commands.md#opencode). GitHub Copilot is
`later` ([01b](01b-harness-commands.md#github-copilot-later)).

**Usage belongs to accounts, not harnesses.** A harness can draw on several
accounts (OpenCode: Anthropic, OpenAI, Google and Copilot by OAuth, DeepSeek
and Moonshot by API key), and several harnesses can share one account
(OpenCode on Anthropic OAuth spends the same Claude weekly limit as Claude
Code). Each session is linked to the account it is currently spending from,
and budgets are per account: a weekly % for subscriptions, and for API-key
providers the money spent so far as the tool reports it (all-time, no period),
optionally against one limit the owner sets.

- **C1 Harness inventory:** name, version, path, parser version, and whether
  the version is within `SupportedVersions`. Outside it, usage readings are
  `unknown` and preflight refuses with `harness_version_unsupported`.
- **C2 Account identity and sign-in.** Read from the harness's own status
  screen, never from credential files. C2 also reports, per session, the
  account it is spending from (`SessionAccountLink`; OpenCode can switch per
  session). Checked 2026-09-29:
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
  the hub creates the enrollment token and its one-line install and
  `fleet-agent enroll --token …` command (the install script is plan PR 16).
  Machine admin is v1 (decision 5 of
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md)): add with a token,
  rename, labels, revoke and forget. The owner
  may enter the expected hostname or IP first; the hub then warns if a
  different machine enrolls with that token. The hub still never connects to the
  machine ([0004](../adr/0004-agent-connectivity.md)). Identity is read by a short transient probe at agent
  start and when the harness version changes, so machines don't keep idle
  harness processes just for identity.
- **C3 Probe folder and folder trust.** The agent never edits harness
  configuration, except the one narrow goal-hook case below (decision 3 of
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md)).
  The usage probe (D1) always runs in one folder the agent owns and keeps empty,
  `~/.local/share/fleet-agent/probe/`. Each harness asks about trusting a new
  folder once. The owner answers that question by hand, once per harness, in the
  probe session: `fleet-agent doctor` says which harness is waiting and the one
  command that attaches to the probe session. The agent never answers a trust
  prompt (01b) and never reads a harness's trust list. It learns that a folder is
  untrusted only from the harness's own screen: a probe or a start that ended on
  the trust screen is reported as `FOLDER_UNTRUSTED` (`TRUST_PROMPT_SEEN` is the
  screen itself), one Triage item names the machine, the harness and the folder,
  and nothing is typed. `fleet-agent doctor` makes the same read-only look: it
  starts the harness in the probe folder, reads the first screen, sends no key and
  tears the session down. The weekly report's scratch folder follows the same
  rule: the owner trusts that one folder by hand. Goal hooks are the only case
  where the agent writes harness settings, under the narrower rule of
  [ADR 0024](../adr/0024-pre-build-owner-decisions.md) (see [01d](01d-goal-hooks.md)).

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
  | Cursor `/usage` | plan ("Pro+"), Included, Auto and API % used, one monthly reset date ("Resets Nov 4"), On-Demand on or off. How pacing reads a monthly budget is plan item 0.20 |
  | OpenCode `opencode stats` (CLI, no probe session) | all-time total cost, as the tool counts it, with no window and no reset (`SpendReading`, no period); a total for all providers together if the tool prints no per-provider figure (issue #46 records what it prints) |

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
  size of the developer's windows, and with the `pause-after=<s>` flag
  (`%pause`, `%continue`, `%extended-output`), set at attach or later by
  `refresh-client -f`. Spike 0.5 ran both flags on tmux 3.2a and 3.7c
  (2026-10-08, [results](../../spikes/tmuxtyping/results/README.md)): both
  arrived in tmux 3.2 (its CHANGES file), and 3.1c refuses `-f` but silently
  ignores both under the old `-F` (read from its source), so a version check
  reads `client_flags` and never trusts `-F`. `ignore-size` is set at attach
  (`attach-session -f ignore-size,pause-after=<s>`): turned on later, it does
  not undo a shrink until the next size change. A `%continue` can arrive
  inside a command's `%begin`/`%end` reply, so the parser must accept
  notifications there.
- **E3 Pane lifecycle:** created and exited with exit status. "Stopped without
  DONE" (the one-nudge rule) depends on this.
- **E4 Activity transitions:** active ↔ idle, with the time of last output.
  Never dropped under backpressure; the hub's stuck detection depends on it.
- **E5 Pane summaries:** last lines, ANSI stripped; at most one per pane every
  5 s; droppable.
- **E6 Prompt detected:** `prompt_id`, text, options, destructive flag, source
  (screen pattern or hook) and confidence. Send-to-pane stays disabled for a
  harness until its detection is verified (Claude first).
- **E7 Harness ready**: whether the adapter's ready pattern is showing in
  a pane, as a snapshot field and an event for both edges.
- **E8 Harness hook events**: turn end, session start, Codex interrupt,
  with the harness session id; per-harness events in [01d](01d-goal-hooks.md).
- **E9 Pane ↔ harness session mapping.** Goal hooks record the harness
  session id against the pane's goal registry entry (G5, 02h). This lets G2
  attribute `.claude/active-goal.<session_id>` files to panes, and K3 find
  transcripts. E9 also attributes a goal file in a pane that has no registry
  entry, by the **observed goal** rule (pane working folder and the newest goal
  file, [02h](02h-goal-binding-and-pane-signals.md#observed-goals-v1)): the
  result is a goal with binding source `OBSERVED`, never a registry entry.
- **E11 Pane availability.** Each harness pane reports whether it is **idle**:
  the adapter's ready pattern is showing, no goal is active in it (G2), and
  there has been no output or keyboard input for a configurable time (default 2
  minutes). Idle alone never makes a pane take work. A pane is **available for
  work** when:
  - fleet started it for a goal and that goal is verified merged, or
  - the developer marked it "available for work" on the board (their own
    sessions are never taken over without that mark; the mark has an option,
    off by default, to start each goal with `/clear`).

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
  Found from a tmux format subscription with a `list-clients` read on each
  change; the 2 s poll runs only as a backup until the subscription is
  confirmed (02h E13).
- **E10 Capture pane:** visible screen or last N lines, ANSI stripped by default.
