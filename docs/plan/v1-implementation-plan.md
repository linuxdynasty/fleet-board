# v1 implementation plan (read-only fleet)

- Status: Accepted (approved and merged; tracked as GitHub issues #25 to #48 in the milestone "v1 - read-only fleet")
- Date: 2026-10-02
- Inputs: [design.md](../design.md) milestones, the capability map in
  [01](../specs/01-agent-capabilities.md) (v1/v2/v3 tags),
  [02e](../specs/02e-contract-rules.md) (freeze checklist),
  [02k](../specs/02k-v1-surface-and-cli.md) (which release serves what),
  [04](../specs/04-technology-review.md), [05](../specs/05-insights.md),
  [05b](../specs/05b-observability.md), [docs/flows/](../flows/README.md), ADRs
  [0001](../adr/0001-system-architecture.md), [0014](../adr/0014-storage-confirmed.md)
  to [0024](../adr/0024-pre-build-owner-decisions.md),
  [AGENTS.md](../../AGENTS.md) and [docs/development/](../development/README.md)

The design phase is done: capabilities, contracts, workflows, technology,
insights and observability are merged. This plan turns milestone 1 of
design.md into an ordered list of small PRs, each with its acceptance test,
the agent role that builds it and the skills it needs. It changes no design;
where it needs a decision it says so. Each row is also a GitHub issue; a change
to a row changes its issue in the same PR (section 10).

## 1. What v1 is, and is not

**v1 sees the whole fleet and changes nothing on it.** (The owner still
configures fleet-board itself in v1: which repos to watch, the reserve of each
account, the Codex account mapping and GitHub, and the machine list: add,
rename, labels, revoke and forget; PR 13a. That changes the hub's records, never
what runs on a machine.) Across all four machines
the owner sees every tmux pane and what runs in it, every goal and its state,
weekly usage of every subscription account with its burn-down and waste line,
GitHub issues and PRs for the watched repos, and the health of fleet-board
itself, live in the Usage, Machines and Session views and in `fleet --json`.
A goal the owner starts by hand in their own tmux pane is shown as an
**observed goal** (decision 1 of [ADR 0024](../adr/0024-pre-build-owner-decisions.md)):
fleet-board sees its goal file and reports it, and never starts, nudges or
controls it.

**Where v1 runs.** The dashboard and `fleet` run on the hub machine. To use them
from another machine, forward the port over SSH
(`ssh -L 7080:127.0.0.1:7080 <hub>`, the same port number on both sides, then open
`http://localhost:7080`); sign-in arrives in v3. The hub machine stays awake while
`fleetd` runs: its launchd unit starts it under `caffeinate -s` (or the
equivalent).

In v1 (capabilities tagged v1 in 01):

| Area | Capabilities | Flows |
|---|---|---|
| Connection | A1 enroll, A2 find the hub, A3 connect/snapshot/reconcile, A4 renew, A5 heartbeat, A6 wake and network change, A7 `Bye`, A8 settings | A1-A3, A5, A12 |
| Machine | B1 facts, B2 resource samples | A2 |
| Harnesses | C1 inventory and parser versions, C2 identity and sign-in, C3 probe folder (the owner trusts it once by hand; doctor explains an untrusted one), C4 command catalog | A6, A13 |
| Usage | D1 probe session, D2 usage reading, D3 passive signals | A6 |
| tmux | E1 inventory, E2 control-mode observer, E3 pane lifecycle, E4 activity, E5 pane summaries, E6 prompt detected (notice-only), E9 observed-goal attribution, E10 capture pane (visible screen), E11 availability, E12 harness conditions, E13 developer input | A5, A8, A9 |
| Goals and repos | G2 goal state (including observed goals), H1 repo inventory, H3 branch state, H4 forge CLI sign-in and push access | A8 |
| Local control | L1 local policy, L2 effective settings, M1 local audit, M2 telemetry, M3 doctor | A12 |
| Hub | GitHub sync (W4) read-only, pacing and waste projection (03b, 05), probe assignment and the hub command path to agents (03b, 03f), triage items for the kinds a v1 source can raise (03d), hub API reads and `Watch`, owner configuration writes and machine admin (02j), nightly backup (03f), System health (05b), subscription-efficiency insights (05) | D2, D3, D10, D13, A14 |

**Not in v1** (v2 unless noted): anything that types into or changes a pane
(F1-F8, G3, G4, I1), goal hooks (G1, E8), harness ready level (E7), worktrees
(H2), session logs and attach (K1, K2, J1), local approval (L3), drain (M4),
power state (B3), dispatch, routines, written insights reports and
suggestions, approvals; sign-in and `authenticated` mode, phone pairing and
remote access (v3); harness transcripts K3 (v3).

**Security posture of v1.** The hub runs in `local_trusted` mode, bound to
loopback for the dashboard; agents connect over mTLS on the hub's TLS port
(ADR 0004). No command type that changes a pane exists in the v1 agent, so
the agent's policy is read-only by construction. Pass-through holds: no
root, no credential reads (ADR 0011), no edits to harness config (the probe
runs in one fleet-owned folder the owner trusts once by hand; decision 3 of
ADR 0024), probes type only the status command into a fleet-owned probe session.
The owner merges every v1 PR (section 10).

## 2. Definition of done

v1 is done, and `v0.1` is tagged, when all of this holds on the author's four
machines for seven consecutive days. The hub runs on a laptop kept awake by
`caffeinate -s` in its launchd unit, so no hub-asleep time is excluded from any
item. The dashboard and `fleet` are checked on the hub machine; another machine
reaches them through `ssh -L 7080:127.0.0.1:7080 <hub>`.

1. Every machine is enrolled and `ONLINE`; a hub restart and a laptop sleep
   each reconnect without loss (`EventCounters.evicted_total` stays 0).
2. For each account, the Usage view's numbers match what the harness's own
   `/usage` or `/status` screen shows at the same time, or show `UNKNOWN`,
   never a guess (checked by hand three times a day for three days).
3. The Session view lists every pane on every machine with the right harness
   within 5 s of a change. A goal started by hand in a pane (eshu goal file) shows
   as an observed goal with the right state, and ends when the file says `DONE` or
   the pane exits.
4. Work items and change requests for the watched repos match GitHub, with
   sync lag under 5 minutes, inside the installation's rate limit.
5. The System section lists all ten signals (05b): nine are measured, and
   `LOG_STORE_USAGE` shows `UNKNOWN` because `logs.db` arrives in v2. It raised
   a SYSTEM triage item when an `always-on` machine was taken offline on purpose
   for over 10 minutes (05b `AGENT_CONNECTED` counts only such machines and
   machines with a live goal), and `BACKUP_AGE` reads the nightly backup.
6. `fleet --json`, run on the hub machine, returns machines, accounts, goals
   (observed ones included) and work items. `fleet machine add`, `rename`, `label`,
   `revoke` and `forget` work and leave audit rows.
7. The four items of the 02e freeze checklist are ticked (PR 17). `v0.1` freezes
   only the messages v1 implements; v2 and v3 messages stay changeable until they
   ship (02e). CI runs `buf breaking --against '.git#tag=v0.1'` from then on, with
   per-path ignores for the unfrozen files.

## 3. Before the first line of product code

These are cheap and unblock everything; each is its own PR or issue. **Prove
first** (AGENTS.md, Evidence Rules): items 0.5 to 0.9 settle a theory the specs
still mark "to be confirmed", and the PR that builds on it waits for the result,
which is written into the spec the item names.

| # | Item | Why |
|---|---|---|
| 0.1 | Fixtures of the screens each parser and detector reads, for the installed versions of Claude Code, Codex, Muse and OpenCode, captured with the `fleet-live-probe` protocol: usage and status screens, command catalogs, startup screens (update, release notes, trust, sign-in), prompt screens, condition screens (for example Muse "HTTP 500 retrying"), banners and status lines, and for OpenCode `providers list`, the model line and `stats` | D2 parsers, C4 catalogs, E6 and E12 detection are fixture-tested (01, `fleet-harness-adapter`); harnesses auto-update (Muse moved to 1.4.2 on 2026-10-02) |
| 0.2 | Owner creates the GitHub App (the read-only permission set of ADR 0022, which adds Contents: read) and installs it on the watched repos; `fleetd setup github` (PR 13a) writes the manifest, until then by hand. The App is the only GitHub access in v1: there is no fine-grained token fallback (decision 15 of [ADR 0024](../adr/0024-pre-build-owner-decisions.md)) | W4 sync needs an installation; only the owner can do it |
| 0.3 | **Decided** (decisions 12 to 14 of ADR 0024; issue #27 records it): binaries come from GitHub Releases with a checksum file, fetched by an install script; `fleet-agent upgrade` and `fleetd upgrade` fetch and verify the next release; no Apple signing or notarization for now (the owner clears the quarantine flag once per machine). Units: a launchd LaunchAgent on macOS (the owner is logged in; on the hub it runs `fleetd` under `caffeinate -s`); a `systemd --user` unit on Linux, with the owner running `sudo loginctl enable-linger <user>` once per worker so it survives logout (`fleet-agent doctor` checks it). Whether the release build is a plain `go build` matrix or GoReleaser is PR 16's call, inside these rules | Packaging PR 16 depends on it; not covered by ADR 0020 |
| 0.4 | Owner sets `MUSE_NO_AUTO_UPDATE=1` on fleet machines, or accepts parser drift handled by `parsing_live` (ADR 0018) | Muse's launcher updates itself in the background (01d) |
| 0.5 | **tmux probe:** does `client_activity` reach a control-mode client (push) or `refresh-client -B` subscriptions, so typing needs no 2 s `list-clients` poll; the release that introduced `pause-after` and `ignore-size` (the minimum tmux is 3.2); how 3.2 is installed on a CI runner. Run on 3.2 and the latest | PR 7 (E13, E2) builds on it; 02h E13 and 01 say "to be confirmed". Result goes into 02h and 01. If there is no push source, the 2 s `list-clients` poll stays: decision 9 of ADR 0024 allows it, and the 60 s goal-file rescan, as the two timed checks outside "poll only usage and GitHub" (owner decision 18) |
| 0.6 | **Wake and network-change probe** on macOS and Linux without cgo (wall-clock jump plus interface polling, or a named library) | PR 4 (A6) builds on it; 01 says "to be confirmed in a spike" |
| 0.7 | **mDNS check:** `hashicorp/mdns` advertising from a macOS hub next to the system responder, and browsing from the agent; record the re-query interval and browse timeout in defaults.md and ADR 0023's open questions | PR 4 and PR 5 (ADR 0023); decides whether the exec fallback is ruled out |
| 0.8 | **Probe cost:** weekly percent before and after N `/usage` or `/status` probes and an idle harness start, per harness, recorded with the harness version in verified-facts.md | design.md, 03b and flows say "probing costs no quota"; nobody measured it. If a harness spends quota, the 10-minute default becomes a per-harness number before PR 8 |
| 0.9 | **Probe folder trust (C3):** for each harness, capture the trust screen of a new folder (a fixture), and check what the harness shows on a trusted and an untrusted folder, whether trusting a parent covers new children (the weekly report's scratch folder), and the exact steps the owner follows once per harness. The agent edits no harness config in v1 (decision 3 of ADR 0024), so nothing is written or restored | 01 C3 now names the mechanism (one fleet-owned folder, trusted by hand, `FOLDER_UNTRUSTED` read from the harness screen); this item supplies the screens and the wording `fleet-agent doctor` prints |
| 0.10 | **Toolchain:** `golangci-lint` v2 built with the Go 1.27 toolchain (the installed one is built with 1.26 and refuses a `go 1.27` module), pinned in the Makefile and CI; Node 24 LTS or an accepted 22 after one `npm run build` | PR 1's gate cannot pass without it (ADR 0020) |
| 0.11 | Issue #46: probe for a usage push source. Not blocking: PR 8 ships D3 as specified; a live source lands as a follow-up PR before `v0.1` or is parked for v2 | owner decision 18 (stream where a push source exists) |

What only the owner can do is 0.2 (create and install the GitHub App, #26) and 0.4
(decide Muse auto-update, #28); 0.3 is decided (above). 0.1 and 0.5 to 0.10 are
agent work.

## 4. Repository layout

One Go module (ADR 0001, 0020), generated code under `gen/` (02e). This tree
refines ADR 0001's package list (ADR 0022): the hub and agent packages below
replace it, and harness adapters stay at the top level as `internal/harness/`:

```
cmd/fleetd  cmd/fleet-agent  cmd/fleet
internal/
  telemetry/            OTEL setup, slog, metric names (05b)
  store/                sqlite open, single writer, migrations (ADR 0014)
  hub/
    config/             fleetd.yaml load and validation, state directory (02j)
    gateway/            AgentService: Enroll, Connect, RenewCert, SessionLog, Terminal; mDNS advertise
    ca/                 fleet CA, enroll tokens, cert serials, revocation list
    ingest/             snapshot apply, event apply, derived state, v1 triage raise and retire, audit rows, housekeeping (03d, 03f)
    outbox/             commands to agents: outbox, acks, results, expiry (03f)
    pacing/             usage readings, pacing, waste projection (03b, 05); pacing/probe: probe assignment and failover
    integration/github/ GitHub adapter: typed REST wrapper + ghinstallation (ADR 0020)
    syncer/             polling loop, cursors, validators, rate limits (03j)
    api/                FleetService (Connect), Watch fan-out (02b, 02c)
    system/             System health signals and triage (05b)
    insights/           rollups (05), v1: subscription efficiency only
  seed/                 test seed: a recorded agent stream replayed into a real hub store (PR 12)
  agent/
    conn/               discovery, connect, backoff, epoch, outbox reader
    ring/               event ring, command log, seq persistence (agent.db, 02i)
    tmux/               control-mode observer, inventory, capture
    probe/              probe sessions on the fleet-probe server
    goals/              goal files, registry observation, repo and branch state
    policy/             local policy, settings merge, local audit
    exec/               command executor: ack, progress, result, dedupe, rate limit (02e, 02i)
    doctor/
  harness/{claude,codex,muse,opencode}/  detect, catalog, parsers + testdata
web/                    React + TS + Vite (ADR 0007, 0020); dist embedded in fleetd
hooks/                  v2: the one Codex-format goal-hook definition and the Muse plugin package (ADR 0001, 01d)
```

Every Go package carries `doc.go`, `README.md` and a scoped `AGENTS.md`
(AGENTS.md, Documentation Discipline).

## 5. The PR sequence

Each row is one PR (or a small epic of PRs where noted), ordered by
dependency. **Role** is the cheapest tier that fits (`.agents/roles.json`);
the orchestrator escalates to `-deep` only for the reasons in AGENTS.md.
Every PR follows AGENTS.md: failing test first for bugs, contract proof for
new code, `fleet-code-review` before push, the full local gate once.

| PR | Scope | Depends on | Acceptance test | Role | Skills |
|---|---|---|---|---|---|
| 1 | **Scaffolding:** `go.mod`, `Makefile` targets (`gen`, `test`, `lint`, `gates`; `gen/` is never committed, so every gate and the worktree recipe start with `make gen`, and `make gates` prints the tool versions it used), `buf generate` into `gen/`, CI workflow (buf build/lint, go vet/test/race on changed packages, golangci-lint, `scripts/check-file-cap.sh`, agent canon with its attribution check on commits and the PR body, docs-stale as an advisory step), empty `cmd/` binaries that print versions; a client-side guard, `.githooks/pre-push` wired by `core.hooksPath`, that refuses a push to `main` and runs the local gate (decision 2 of ADR 0024: the repo is private on a plan without branch protection, and the owner merges every PR). Files at the cap (02-contracts.md, 02b, 01, 03c, status.proto, two flows files) are split in the first PR that has to add to them | 0.10 | CI green on an empty build; generated Go and TS compile; a seeded 500-line file and a model `Co-Authored-By` trailer each fail CI; with `core.hooksPath` set, a push to `main` is refused and a push of a branch runs the gate | develop-fleet | fleet-go, fleet-contract-rigor |
| 2 | **Telemetry and store foundations:** `internal/telemetry` (slog JSON, OTEL providers, no exporter by default, `/metrics` handler); `internal/store` (open with the ADR 0014 pragmas, single writer goroutine, 250 ms / 100-message batches, forward-only migrations, read pool) | 1 | writer batching and durability tests; a fullfsync check on darwin like spike S1 | develop-fleet-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 2b | **`fleetd` shell:** `fleetd.yaml` load and validation with the 02j defaults, `fleetd init` (config and state directory), the agent TLS listener with the server certificate and its SANs (`tls.agent_names`), `admin.sock` (mode 0600) with `fleetd enroll-token`, `fleetd run` | 2 | a fresh `fleetd init` then `fleetd run` starts, refuses a non-loopback API address in `local_trusted`, and `fleetd enroll-token` prints a token | develop-fleet | fleet-go, fleet-security |
| 3 | **Fleet CA and enrollment (hub):** CA and `hub_identity` row, enroll tokens with CA hash, `Enroll` (retry-safe), cert serials, revocation lookup and the `revoked-serials` file; the nightly `VACUUM INTO` backup into `backups/` (mode 0600; keep the last 7, made at 03:00 hub local time: [defaults](../specs/defaults.md), decision 16 of ADR 0024) and `fleetd backup [--encrypt]` (03f: a backup holds the CA key; `--encrypt` uses `age`), which is what `BACKUP_AGE` reads | 2b | enroll, replayed enroll, expired token, wrong CA hash refused; a backup opens, holds `hub_identity`, and is mode 0600 | develop-fleet | fleet-security, fleet-go |
| 4 | **Agent connection:** `fleet-agent enroll` and `run`, discovery (cached, mDNS through `hashicorp/mdns`, configured: ADR 0023), mTLS connect, `Hello`/`HelloAck`, epoch, backoff with jitter, wake and network-change detection (0.6), `Bye`, `RenewCert`; `agent.db` with seq high-water mark and command log; a minimal facts stub in `Hello` until PR 6 | 3, 0.6, 0.7 | in-memory hub fake: connect, reconnect after hub restart, seq never goes backwards, duplicate identity displaced; advertise and discover on a macOS hub with the system responder running, re-query interval and browse timeout recorded in defaults.md | develop-fleet-deep | fleet-go, fleet-sqlite-store |
| 5 | **Hub gateway and ingest:** `Connect` server, snapshot apply in one tx, event apply with delivery classes, heartbeat and `last_seen` cadence (written at most once a minute: defaults.md), `EventCounters`, machine state ONLINE/STALE/OFFLINE, mDNS advertise on the agent port; **command path** (`internal/hub/outbox`): outbox and `command_results` tables, send with epoch and seq, `CommandAck`/`Progress`/`Result`/`ResultAck` apply, `not_after_time` expiry, `SnapshotRequest`, `Operation` store; settings send and `SettingsAck`; the writer stamps `hub_seq`, `version`, `observed_time` and writes `audit_log` rows; **triage** raise and retire in the event transaction for the v1 kinds (prompt, condition, parser_degraded, usage_parse_failed, parser_unsupported, policy_invalid, policy_changed, hostname_mismatch, duplicate_identity, settings_unenforced; PR 10 raises `pacing` and PR 11 `integration_access_lost` with the same code, PR 13 the `system` items; kinds whose source is v2 are not raised) with the 03d fingerprint and 7-day expiry; the housekeeping job (STALE and OFFLINE marking, retention per table, outbox expiry: 03f); `fleetd restore` with the 24 h reconcile flag (03f, 02g) | 2, 3, 4 | agent fake against real hub: snapshot then replayed events, shed counted not lost, STALE at 45 s, an expired command ends `EXPIRED`, a duplicate `command_id` runs once; a seeded condition raises one deduplicated triage item and retires it; each retention table prunes in a test; after a restore agents reconnect with the pinned CA and the hub adopts what they know | develop-fleet-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 6 | **Machine facts, policy, command executor, doctor:** B1, B2 (sampling with gopsutil: ADR 0023), L1 local policy file with change detection (every key, type and default is in 03e; a missing file is a normal first run, and `fleet-agent enroll` asks for the repos folder and writes a starter `policy.yaml`), L2 tighten-only merge, M1 local audit, M3 `fleet-agent doctor` (including the probe-folder trust check per harness and, on Linux, the linger check), C3 (the probe folder, 01); the agent **command executor** (`CommandAck`/`Progress`/`Result`, deadline from `HelloAck.hub_time`, duplicate `command_id` returns the logged result, an unknown kind answers `UNSUPPORTED`, per-kind rate limit) | 4 | policy refusals audited; a fresh agent with no `policy.yaml` starts, reports an empty `allowed_repo_roots` and allows every installed harness; `enroll` writes the starter file; doctor lists every check with a reason; a replayed command returns its logged result; the sampler checked on the four machines | develop-fleet | fleet-go, fleet-security |
| 7 | **tmux observer (epic, 2-3 PRs):** E1 inventory, E2 control mode, E3 lifecycle, E4 activity, E5 summaries (latest per pane, persisted), E10 capture (visible screen), E11 availability (idle reporting; the developer's mark is a v2 command), E13 developer input as 0.5 decides; internal sessions marked | 5, 0.5 | against real tmux 3.2 and the latest release in CI (3.2 installed as 0.5 found); `pause-after` and `ignore-size` gated by version; fakes for unit tests | develop-fleet-deep | fleet-tmux, fleet-go |
| 8 | **Harness adapters and usage probes (epic):** C1-C4, D1 probe sessions on the `fleet-probe` server, D2 parsers for Claude `/usage`, Codex `/status`, Muse `/usage` with fixtures from 0.1, D3 passive signals, `VersionSupport`/`ParserSupport`/`parsing_live`, E6 prompt detection (notice-only), E12 conditions; the OpenCode adapter (inventory, `providers list` for account discovery, model line, spend from `stats`; no usage window, no `/goal`); the probe runs in the fleet-owned probe folder (C3; the agent edits no harness config); a probe pane on a startup screen (trust, update, login, release notes) is `alive=false`, `STARTUP_PROMPT_SEEN`, no key typed, failover tried, and one triage item says how to answer it; a trust screen is reported as `FOLDER_UNTRUSTED` from the harness's own screen | 7, 0.1, 0.8, 0.9 | every fixture parses or returns `UNKNOWN`; never a guessed number; probe types only the status command; the Codex update-screen fixture yields the startup-screen failure and no key press; a fixture of each harness's trust screen yields `FOLDER_UNTRUSTED` and no key press; `fleet-agent doctor` run on that screen prints the one-time step for the owner | develop-fleet (parsers), develop-fleet-deep (probe session control) | fleet-harness-adapter, fleet-tmux, fleet-live-probe |
| 9 | **Goals and repos (observe only):** G2 goal state from eshu goal files (`.claude/active-goal.<session_id>`; the generic file is v2), E9 **observed goals**: a goal file found by the pane's working folder and the newest goal file ([02h](../specs/02h-goal-binding-and-pane-signals.md#observed-goals-v1)), reported with binding source `OBSERVED` and an agent-minted id, never dispatched, nudged or controlled, ended by `DONE` or pane exit; the 60 s goal-file rescan; H1 repo inventory and `RepoRef` derivation, H3 branch state, H4 `gh` status | 7 | eshu goal-file fixtures; a goal file in a pane's folder yields one observed goal with the right state, a second harness pane in the same folder yields none (ambiguous, never guessed), `DONE` and pane exit each end it, a restart of the agent keeps its id, and no command or nudge is ever sent to it; a dirty checkout and a detached HEAD reported correctly | develop-fleet | fleet-go |
| 10 | **Usage, pacing, probes and accounts (hub):** usage readings, account discovery and Codex mapping, `AccountPacing` with burn-down and `projected_unused_percent`, which is unset until 24 hours of the usage window have passed (worked examples and edge cases in 03b); API-key accounts show "spent so far" as the tool reports it, with no cap period, `reading_stale`, `insight_pacing_interval` rows on each change; **probe assignment** (`internal/hub/pacing/probe`): the `probes` table with its unique-active constraint, candidate rules and preference, failover on OFFLINE or 3 failures, `StopProbe` reconcile from the snapshot, the passive-reading `run_now` trigger, a 30 s tick | 5, 8 | pacing math table tests from 03b and 05, including no projection before hour 24 of a window; stale reading after a passed reset; two passes never assign one account twice; failover on OFFLINE and on 3 failures; `run_now` at most once a minute per account | develop-fleet | fleet-sqlite-store |
| 11 | **GitHub sync (read-only):** REST for issues with ETags, GraphQL for change requests and timeline evidence (batched), installation tokens, conditional requests, rate-limit handling, cursors, `ProviderError`, work item and change request mirrors, the 5-minute claim loop, the trust verdict and stage computed in the same transaction as the item upsert (stored, never acted on in v1), change request found by the agent-reported `BranchState.branch`, Repo resource, and the `Integration` row created from the `github:` keys of `fleetd.yaml`. GitHub App access only: there is no fine-grained token fallback in v1 (decision 15 of ADR 0024) | 2b, 0.2 | recorded HTTP fixtures; 304s not counted; rate-limit back-off; sync lag metric; a trusted and an untrusted item get the right stage | develop-fleet | fleet-go, fleet-security |
| 12 | **Hub API reads and Watch:** the v1 read set of [02k](../specs/02k-v1-surface-and-cli.md) (machine, session, `CapturePane`, goal and runs, work item, change request, repo, account, usage readings, triage, integrations, settings, audit entries, store status, `GetOperation`, `FleetSummary`, `GetServerInfo`); filters with the 02c field catalogue, paging, the change log, coalescing and per-principal rate limits; `Watch` with per-topic subscriptions, resume tokens, `RESYNC_REQUIRED`, latest-wins summaries; every RPC and topic of a later release answers `UNIMPLEMENTED` or is refused (02k); the **seed package** (`internal/seed`); a placeholder `web/dist/index.html` so `go build` embeds; `local_trusted` only, Host and Origin checks, loopback bind | 5, 10, 11 | contract tests per RPC; a filter, a page and a stale `RESYNC_REQUIRED` token tested per resource; Watch resume after reconnect; slow consumer drops summaries only; a v2 RPC returns `UNIMPLEMENTED` | develop-fleet-deep (Watch), develop-fleet (reads) | fleet-contract-rigor, fleet-security |
| 13 | **System health and insights v1:** the ten System signals, `GetSystemStatus`, SYSTEM triage items with hysteresis; subscription-efficiency rollups and `GetInsights` for them; `fleetd insights rebuild` | 12 | each signal driven over its thresholds in a test (`LOG_STORE_USAGE` with an injected value; it reads `UNKNOWN` on a real v1 hub, which has no `logs.db`); rollup equals a hand-computed week | develop-fleet | fleet-telemetry-coverage |
| 13a | **Owner configuration:** the write RPCs of [02j](../specs/02j-hub-runtime.md#configuring-a-fresh-hub) behind `SETTINGS` and `request_id`/`expected_version`: `CreateRepo`/`UpdateRepo`/`DeleteRepo` (watched repos, caps, consent), `CreateAccount`/`UpdateAccount`/`DeleteAccount` (`reserve_percent`, default 10; Codex `bindings`; a new account starts with consent push, pr-open, merge), `UpdateIntegration` (label, tracker projects), `UpdateSettings`, `ExportConfig`/`ImportConfig` with `plan_hash`, `SnoozeTriageItem`/`DismissTriageItem` (hub state only); the idempotency store and update-mask validation; every write audited; `fleetd setup github` (steps in 02j: it writes the App manifest and the `github:` keys); and **machine admin**, confirmed for v1 (decision 5 of ADR 0024): `CreateEnrollmentToken` (the "add machine" command with its token), `UpdateMachine` (rename, labels), `RevokeMachine`, `DeleteMachine` (forget) | 10, 11, 12 | a fresh hub is configured only through these calls (no SQL, no hand-edited database; the dashboard forms are PR 14, the CLI is PR 15): watch a repo, set a reserve, map a Codex account, import a `FleetConfig` plan (dry run, then apply, then a stale `plan_hash` refused) and apply it; a stale `expected_version` is refused; an enrollment token is created and an agent enrolls with it, a machine is renamed and labelled, a revoked machine's certificate is refused at its next handshake, and `DeleteMachine` is refused until the machine is revoked; every write leaves an audit row; the Watch topics `REPOS`, `ACCOUNTS`, `SETTINGS` show each change; `setup github` runs against recorded GitHub responses and writes its key file mode 0600 | develop-fleet | fleet-contract-rigor, fleet-security, fleet-go |
| 14 | **Web UI (epic):** Vite scaffold, generated client, Watch hook, then views in this order: Machines (+ System), Usage, Session (read-only panes, no attach: the last lines of each pane from `PaneSummary.last_lines`, plus the visible screen on demand with `CapturePane`), Triage (read-only: prompts notice-only, conditions, system; snooze and dismiss), Work (read-only board), Insights (efficiency only), Settings (the forms for the writes of PR 13a); `web/dist` embedded in `fleetd`; the router and styling choice gets its ADR 0020 table line first; the mockup is marked display-only and its v2 and v3 controls are listed (ui-contract-map) | 12, 13, 13a | vitest per component; Playwright smoke per view against `fleetd` started on the seed; phone width; each view shows only its v1 subset | develop-fleet | fleet-frontend |
| 15 | **CLI `fleet` (read, and the writes of 13a):** `fleet status`, `machines`, `accounts`, `goals`, `work`, `doctor`, all `--json`; `fleet repo`, `fleet account`, `fleet settings`, `fleet machine` (add, rename, label, revoke, forget), `fleet export`, `fleet import --dry-run` (the `fleet.yaml` renderer and parser with short enum names and `format_version: 1`: design.md); `FLEET_URL`; `FLEET_TOKEN` optional in `local_trusted` (02f). Commands and release tags: [02k](../specs/02k-v1-surface-and-cli.md) | 12, 13a | golden JSON outputs against the seed; the configuration walk of 13a repeated through the CLI; export then import of the same hub is an empty plan | develop-fleet | fleet-go |
| 16 | **Packaging and install:** release builds for darwin/linux amd64/arm64 published to GitHub Releases with a checksum file; the install script (verifies the checksum before it installs); `fleet-agent upgrade` and `fleetd upgrade` (fetch the next release, verify it, swap the binary, restart the service; `agent.db` and `state.db` migrate forward only); launchd LaunchAgent units, the hub's running `fleetd` under `caffeinate -s`; `systemd --user` units with the one-time `sudo loginctl enable-linger <user>` step on Linux workers; no Apple signing or notarization (the runbook gives the one command that clears the quarantine flag); the documented `fleetd init` and `fleetd enroll-token` (`claim` is v3); runbook in docs/, including the SSH forward for using the dashboard and `fleet` from another machine (`ssh -L 7080:127.0.0.1:7080 <hub>`) | all above, 0.3 | install, enroll and upgrade on a clean VM and on the four machines; a tampered archive fails the checksum and installs nothing; a Linux worker still reports `ONLINE` after the owner logs out; the hub stays awake under `caffeinate -s` (a sleep test shows it) | develop-fleet | fleet-go, fleet-security |
| 17 | **v1 acceptance and `v0.1`:** the seven-day run of section 2, fixes found there, the four items of the 02e freeze checklist (every contract-review finding fixed or deferred; generated clients compile in CI; the mockup data checked against the protos or marked display-only; `go.mod`, the generated-code location and the CI job), tag `v0.1` for the surface v1 implements only, with per-path `breaking` ignores in `buf.yaml` for the v2 and v3 messages (02e), `buf breaking` in CI | 16 | section 2 met; review by `review-fleet-deep`; `arbiter-fleet` ruling before tagging | review-fleet-deep, arbiter-fleet | fleet-code-review |

Parallel lanes: after 2b, PR 3 and PR 11 are independent; after PR 5, 6 and 7
are independent of each other; 8 and 9 follow 7; 10 follows 8; 12 waits for 10
and 11; 13a follows 10, 11 and 12; 14 can start its scaffold against the mockup
while 12 is in review. The 0.x probes run first and in parallel.

## 6. How it is tested

- **Fakes at every edge** (01c testing approach): tmux, harness screens, clock,
  network, power, file watching. Hub and agent are tested against an
  in-memory fake of each other over a real gRPC connection, so no `sshd`, keys
  or machines are needed for CI.
- **Parser fixtures per harness version** under `internal/harness/*/testdata/`;
  a new harness version without fixtures is `UNVERIFIED_PARSING`, never assumed.
- **Store tests** run the real SQLite driver on disk, including a durability
  check like spike S1 on the CI runner's filesystem.
- **tmux** runs for real in CI at 3.2 and the latest release.
- **Contract tests** per hub RPC and per agent command kind, generated from
  the protos so a renamed field fails the build before v0.1 and `buf breaking`
  after it.
- **Web**: Vitest for components, Playwright for one smoke flow per view
  against `fleetd` started on the seed package (PR 12, [02k](../specs/02k-v1-surface-and-cli.md)).
- **Telemetry**: every never-dropped event kind and command kind has its
  counter (03, `fleet-telemetry-coverage`); a PR adding one without the other
  fails review.

## 7. Keeping development cost low

- **Tiers from `.agents/roles.json`:** `scan-fleet` (fast) for evidence
  gathering; `develop-fleet` and `review-fleet` (workhorse) by default;
  `-deep` only for PRs 2, 4, 5, 7, the probe-session half of 8, the Watch half of 12, and fixes a
  workhorse attempt got wrong; `arbiter-fleet` only for design forks, disputed
  findings and the `v0.1` tag.
- **Small PRs** (one row or sub-row of section 5), so reviews stay cheap and a
  failed attempt costs one PR, not a milestone.
- **Gates run once.** Subagents run focused tests; only the orchestrator runs
  the full local gate, right before push (AGENTS.md).
- **Specs are the context.** A handoff names the spec sections, flows and
  protos a PR implements; agents do not re-read the whole design.
- **fleet-board drives itself** as soon as v2 can dispatch: its own issues
  become goals on spare subscription quota.

## 8. Risks and how v1 handles them

| Risk | Handling |
|---|---|
| A harness auto-updates and its screens change | `parsing_live` keeps dispatch-relevant parsing going (ADR 0018); fixtures are refreshed with the probe protocol; `UNKNOWN` is shown rather than a guess |
| Provider terms on automation ([terms-of-service.md](../research/terms-of-service.md)) | v1 only runs each harness's own status command in an idle probe session the owner can see; no headless use, no credential reads; written confirmation from providers is still needed before offering fleet-board to others (design.md) |
| Laptop hub sleeps | the hub's launchd unit runs `fleetd` under `caffeinate -s`, so it stays awake while it runs (PR 16, decision 12 of ADR 0024); if it still stops, agents back off and reconnect and the ring replays; acceptance criterion 1 tests it |
| tmux versions differ (3.2 to 3.7 on the fleet) | CI runs both ends; control-mode features gated by version |
| GitHub rate limits | conditional requests and the 26 % budget measured in 04; sync lag is a System signal |
| Store durability on a laptop | `fullfsync` on darwin, proved in spike S1 and re-checked in PR 2 |
| Scope creep into v2 | the v1 agent has no pane-changing command kinds at all; adding one is a v2 PR |

## 9. Work owed to v2 (not blocking v1)

Probes and measurements already recorded as open in the specs, to schedule
before the v2 PRs that need them:

- Generic goal-file location writable inside each harness's sandbox without
  dirtying the checkout (01d).
- Muse `SessionStart` timing for the session-id registry fallback (01d).
- Re-arm after `DONE` on Codex and Muse, and resume after a usage limit on all
  three harnesses (01b, 02h).
- Compression of Muse and Claude Code goal hours under the 5-minute framing,
  and `logs.db` at the 50 GiB cap (04, "Not proven"). Logs are v2 (K1, K2), so
  both re-runs gate the first logs PR instead of `v0.1`; 04's table says the same.
- Cost of the weekly insights report (05, `NOT_MEASURED`).

Owner decisions deferred, recorded as open and not decided
([ADR 0024](../adr/0024-pre-build-owner-decisions.md)):

- **v2:** in `local_trusted` mode any process on the hub machine is the owner, so
  approvals and settings changes there may need a local secret (decision 20).
- **v2:** the default goal text for repos without eshu's hooks is drafted in v2
  and approved by the owner before it goes into 03c (decision 21).
- **v3:** phone push; the lean is an installable web app with web push and no app
  store (decision 22).

## 10. Tracking

Each row of section 5 is a GitHub issue (epics for 7, 8 and 14) with its
acceptance test copied in, in the milestone "v1 - read-only fleet" (#25 to #48;
#46 is the usage push probe, 0.11; #48 is PR 13a). Section 3 items are issues
assigned to the owner where they need a decision. Work follows
`fleet-issue-driver`: a worktree per issue, one PR per change, self-review of
the full diff, CI read twice before claiming done.

**Who merges.** The owner merges every v1 PR (decision 2 of ADR 0024). No agent
merges, and no `arbiter-fleet` ruling stands in for the owner's merge. Nothing
mechanical protects `main` on this private repository (branch protection needs a
public repo or a paid plan), so PR 1 adds a client-side guard: `.githooks/pre-push`,
turned on by `git config core.hooksPath .githooks`, refuses a push to `main` and
runs the local gate. Until PR 1 lands, AGENTS.md is the only guard.

**The plan and its issues change together.** A PR that changes a row of this
plan edits that row's issue (scope, dependencies, acceptance, title) in the
same PR, and the PR description says so; `fleet-issue-driver` checks it before
declaring a PR ready. Each bullet of section 9 gets one v2 issue, or one
checklist issue linked here, and v2 and v3 get a milestone with an epic issue
each.
