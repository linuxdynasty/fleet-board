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
   policy ([0004](../adr/0004-agent-connectivity.md)). Every key typed into a
   pane is a line built by one package, `internal/agent/tmux/typing`, and lint
   rules keep it that way ([01f](01f-agent-control-goals-and-policy.md) F3).
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
8. **Every short program the agent runs is bounded.** A one-shot child process
   (`git`, `gh`, `loginctl`, a version check) gets empty stdin, a time cap and
   an output cap (stdout and stderr together), runs in a process group of its
   own, and is killed, group and all, when a cap passes or its caller gives up.
   All of them run through one helper, `internal/proc`. Each caller's caps are
   set from a measured run of its command and recorded in the
   [defaults](defaults.md) row "One-shot child process"; a caller with no entry
   there gets no default, and none has an entry until the row that adds it
   (plan rows 6, 7, 8) measures it. Every kill is logged and counted with its
   cause ([05b](05b-observability.md)). A lint rule lets only `internal/proc`,
   the `internal/agent/tmux` package itself and `internal/agent/attach` import
   `os/exec` (or `golang.org/x/sys/execabs`), and only `internal/proc` start a process any other way. The
   long-lived `tmux -C` client and the attach PTY are not one-shot.

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
- **A5 Heartbeat** every `HelloAck.heartbeat_interval`, 1 to 15 s, default
  15 s: load, raw counts (fleet panes, panes by harness,
  goal panes by goal state), wall clock and monotonic uptime for skew
  detection. The hub computes slots; the agent computes two aggregate goal counts
  for its own safety cap (02h).
- **A6 Wake and network change** triggers a reconnect after a short random
  delay (03 A2). It only makes reconnecting faster: when a wake or a change is
  missed, transport keepalive (ADR 0004) and the reconnect backoff still bring
  the agent back, and the snapshot on connect (A3) puts the state right. Wake:
  every 5 s the agent reads how long the machine has been asleep since boot
  (Linux `CLOCK_BOOTTIME` minus `CLOCK_MONOTONIC`, macOS `CLOCK_MONOTONIC_RAW`
  minus `CLOCK_UPTIME_RAW`) and compares how far the wall clock moved with how far
  Go's monotonic clock moved (that clock stops while the machine sleeps); either
  growing by 2 s or more since the last check is a wake; a wall clock stepped
  back 2 s or more is logged and is not a wake. Network change: the routing
  push socket (macOS `PF_ROUTE`, Linux `NETLINK_ROUTE`, no root) is only a
  hint; the agent acts only when its list of interfaces, addresses and default
  route really changed, because Linux re-announces unchanged IPv6 addresses
  every few seconds to minutes; a failed read is retried, so the change behind
  a hint is not lost. While connected, only the loss of the
  connection's local address (the previous reading held it and the new one
  does not; an address the agent does not know never counts as lost) or a
  changed default route reconnects; while not connected, any real change
  resets the backoff. No cgo, no `exec`. Values:
  defaults.md; methods and evidence: ADR 0029 and spike 0.6.
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
  schema version. `container` is true when `/.dockerenv` or
  `/run/.containerenv` exists. tmux is looked up on the agent's PATH, then
  in `/usr/local/bin`, `/opt/homebrew/bin` and `/usr/bin`, because launchd and
  systemd start an agent with a minimal PATH; none found leaves
  `tmux_version` empty.
- **B2 Resource samples** every 30 s: load, memory pressure, free bytes under
  each repo root and the state directory.
  A reading the agent could not take is left unset in `ResourceSample` (its
  load and memory fields are `optional`) and in `Heartbeat.load1`, never sent
  as 0, and `disk` lists only the paths that were read; a sample older than
  three intervals is not sent (defaults). Row 6 sends the latest sample in
  every `Snapshot`; the `resource_sample` event is row 7's.
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
  `unknown` and preflight refuses with `harness_version_unsupported`. The
  version comes from the harness's own version command, run as principle 8
  says; its first line must hold a dotted version number such as `1.4.2`, or
  there is no version: `not_on_path`, `empty_version_output` or
  `invalid_version_output` (a wrapper that exits 0 and prints an install prompt
  is not a version), counted as a failed `Detect()` (03e, W11).
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
  `~/.local/share/fleet-agent/probe/`. Most harnesses ask about trusting a new
  folder once (OpenCode never; Codex only for a git repo, as seen with command-line settings only; the probe record is
  [01b](01b-harness-commands.md#folder-trust-probe-2026-10-10)). The owner
  answers that question by hand, once per harness, in a terminal of their own:
  `fleet-agent doctor` says which harness is waiting and prints the steps in the
  table below. Trust belongs to the folder, not to a tmux session, so the owner
  needs no attach command for the trust step: a terminal of their own is enough
  (other startup screens, such as update or login, may still need the pane).
  The agent never answers a trust
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

  **The one-time owner steps, and the text `fleet-agent doctor` prints.**
  `<folder>` is the probe folder (or the weekly report's scratch folder), printed
  in full. Doctor prints a harness's block only when it saw that harness's own
  trust screen in `<folder>` (probe record: 01b). Doctor does not say a folder is
  "untrusted" unless it saw the screen; otherwise it says "not checked".

  | Harness | Doctor prints (plain words) |
  |---|---|
  | Claude Code | "Claude Code is waiting for you to trust `<folder>`. In a terminal run `cd <folder> && claude`. When the box 'Accessing workspace' appears, it starts on 'No, exit': press Down first, choose 'Yes, I trust this folder' and press Enter. Then type `/exit`." |
  | Codex | "Codex is waiting for you to trust `<folder>`. In a terminal run `cd <folder> && codex`. If it first asks about an update, read it and skip it (Esc skips it, do not choose 'Update now'). If it asks about a background server, read it and choose '1. Run without daemon this time', never '2. Restart with these settings'. On 'Folder access' choose '1. Trust and continue'. Then type `/exit`." Codex asks this only inside a git repo (seen with command-line settings only; YOLO off in `config.toml` is not proven), so for the plain probe folder this block normally never shows. The probe recorded Esc only on the update prompt; on the background-server prompt it tried only option 1. |
  | Muse | "Muse is waiting for you to trust `<folder>`. In a terminal run `cd <folder> && muse`. On 'Do you trust this workspace?' choose '1 Trust and continue'. Then leave Muse the way you usually do." |
  | Cursor | "Cursor Agent is waiting for you to trust `<folder>`. In a terminal run `cd <folder> && cursor-agent`. On 'Workspace Trust Required' choose '[a] Trust this workspace'. Then leave Cursor Agent the way you usually do." |
  | OpenCode | Nothing to do: OpenCode has no folder-trust screen. |

  Trust and the folder: Cursor trusts a folder and everything below it; Claude
  Code and Codex appear to follow the git repo root (not proven: Claude Code
  on a plain folder). **Muse trust covers one folder only**: neither a trusted
  home folder nor a trusted repo root covered a plain child (the box still names the repo root, 01b). So the weekly report's scratch folder is
  checked and trusted on its own, never assumed from the probe folder. The
  "yes" answer is the owner's act, once per harness and folder; the typed `/exit`
  is the owner's too, since the fleet never presses a key on a trust screen.

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
  | Cursor `/usage` | plan ("Pro+"), Included, Auto and API % used, one monthly reset date ("Resets Nov 4"), On-Demand on or off. Reported as three `WINDOW_KIND_MONTHLY` buckets and `OverageBilling` ([03b](03b-usage-and-sync.md#monthly-windows-cursor), "Monthly windows") |
  | OpenCode `opencode stats` (CLI, no probe session) | all-time total cost, as the tool counts it, with no window and no reset (`SpendReading`, no period); a total for all providers together if the tool prints no per-provider figure (issue #46 records what it prints) |

  Each reading has two parts:
  - **as shown:** every field the parser recognized, with the harness's labels,
    plus the raw screen text (personal data redacted per C2), for display;
  - **standardized:** a list of limit buckets (label, % used as integer 0–100
    or `unknown`, reset time in UTC, window length if known, and whether it is
    a short window, the weekly budget or a monthly one), for the scheduler. Codex's "% left"
    becomes "% used" here; the display keeps "% left".

  Also: `probe_id`, account key, `read_time`, parser version. Reset times on
  screen are local strings without a year ("Resets Sep 30 at 10pm"); the agent
  resolves them with B1's timezone and sends both the original text and UTC; a
  date with no time (Cursor "Resets Nov 4") resolves to the end of that date
  (03b, "Monthly windows"). On parse failure the reading is `unknown` and the
  redacted raw screen is still shown, never a guessed value.
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
  server (marked "yours" or "started by fleet" from `FLEET_*` tags (read from
  the `@fleet_*` user options tmux holds, never from a process's environment;
  row 18 sets them, and the matching `FLEET_*` session environment for the
  harness and its hooks, when it creates a session or window)), plus the
  internal `fleet-probe` server (internal sessions are marked and the hub
  ignores them). Panes carry their directory, repo and dirty flag when under an
  allowed root; attribute changes are `PaneChanged` events (02h). Windows are reported with their index and
  name exactly as the status bar shows them (developers name windows by
  purpose, e.g. `codex prompt 1`, `claude w2`, `btop`) and whether tmux
  renames them automatically. The harness in a pane is identified from the
  running process (`pane_current_command` and the adapter's `Detect()`), never
  from the window name. Panes are keyed by `(server, server_start_id, pane_id)` plus
  the `FLEET_*` tags read from the `@fleet_*` options (above), because tmux ids
  restart from zero with a new server. Every name, command and option value
  is bounded in tmux itself at 1 KiB in bytes (defaults; row 7 PR 7d1): tmux
  sends a longer value empty with its length beside it, so that field reads
  as blank and is counted (05b), and no value can fail a read. A session is
  internal only when its `@fleet_internal` is exactly `attach_view` (02h).
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
  inside a command's `%begin`/`%end` reply: tmux 3.2a and 3.7c write
  `%pause` and `%continue` inside the reply to the client's own
  `refresh-client -A`. Reply lines are also raw command output (a
  `capture-pane` line is pane text and can start with `%`), so the parser
  keeps every line of a reply as reply text, lifts nothing out, and ends the
  reply only at the `%end` or `%error` with the same time, number and flags.
  The client may honour a `%pause` line found in any reply (pausing is the
  safe direction, and tmux may pause a pane while another command runs), but
  a `%continue` line only when that reply answers its own `refresh-client -A`
  for that pane, so it never marks a pane resumed on a `%continue` it did not
  ask for; the client's tests prove this with a flood during a command. Lines and replies are
  bounded (defaults; each kept reply line also costs its overhead), and a
  line over the bound is skipped to its newline and counted (05b).
  The client (`internal/agent/tmux`, row 7 PR 7c2) attaches with `-E`, so tmux
  does not apply `update-environment` to the developer's session, and with
  `-u`, so names and values come byte for byte whatever LANG the agent has
  (under LANG=C, which a service often gets, tmux would send each non-ASCII or
  control character as `_`), and checks `tmux -V` (below 3.2 is
  `TMUX_TOO_OLD`), attaches to the lowest session id (a session the fleet
  marked internal, its `@fleet_internal` exactly `attach_view` as tmux
  compares it, is second choice: when only internal sessions exist it attaches
  there, and `switch-client -E` moves it to the first developer session that
  `%sessions-changed` announces, so no session is ever unseen and no timer
  runs), reads its own `client_flags` and refuses a tmux that does not list
  both flags, and mints `server_start_id` from the server's pid and start
  time. On `%exit` or a dead stream it attaches again to the then-lowest
  session with backoff and sends its subscriptions again before it reports up;
  with no server on the socket it waits for the socket-folder watch, never a
  timer (defaults). It sends only typed commands and `typing.Line`s, refusing
  a line built for another server or an earlier start of this one. Its
  inventory read is one `list-panes -a`, bounded at 10 s (defaults), whose text fields tmux bounds in bytes
  (E1). Its one known gap is the 8 MiB reply bound: about 13,100 normal panes
  on one server, 119 in the worst case (newline-filled tags; defaults), far
  over the 04 budget of 10 per machine; past it the read fails whole, counted
  (05b). Measured in its tests on tmux 3.2a and 3.7c (macOS, and Linux in a
  container), 2026-10-10: the nested `#{S:#{W:...}}` subscription expands on
  3.2a as on 3.7c, so the per-window fallback of the row-7 ruling fork 5(e) is
  not needed; in the two recorded runs of a pane flooding while the reader
  stalled 3 s during a command (`internal/agent/tmux/testdata/flood-*.txt`),
  tmux wrote no `%pause` and the reply came whole and in order. Further
  unrecorded runs (3 and 8 s stalls) showed no `%pause` inside a reply either.
- **E3 Pane lifecycle:** created and exited with exit status. "Stopped without
  DONE" (the one-nudge rule) depends on this. The status is known only when
  tmux kept the pane (`remain-on-exit`, which the agent never sets on a
  developer's pane); a pane gone first has `exit_status` unset (row 7 PR 7d, `internal/agent/tmux/inventory`; read triggers in defaults). A signal-killed pane has no exit status;
  tmux 3.7c names the signal (macOS) or numbers it (Linux), and both reads,
  the pane list and the inventory, give `hup`, `int`, `quit`, `abort`, `kill`,
  `segv`, `pipe`, `term`, `other:<n>` (the host's number, not comparable
  across hosts) or `other` (unreadable), never tmux's text; 3.2a gives none.
  No snapshot field carries it yet; `TmuxPane` has only `dead` and `exit_status`.
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
