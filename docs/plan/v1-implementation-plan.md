# v1 implementation plan (read-only fleet)

- Status: Proposed for review
- Date: 2026-10-02
- Inputs: [design.md](../design.md) milestones, the capability map in
  [01](../specs/01-agent-capabilities.md) (v1/v2/v3 tags),
  [02e](../specs/02e-contract-rules.md) (freeze checklist),
  [04](../specs/04-technology-review.md), [05](../specs/05-insights.md),
  [05b](../specs/05b-observability.md), [docs/flows/](../flows/README.md), ADRs
  [0001](../adr/0001-system-architecture.md), [0014](../adr/0014-storage-confirmed.md)
  to [0021](../adr/0021-insights-and-observability.md),
  [AGENTS.md](../../AGENTS.md) and [docs/development/](../development/README.md)

The design phase is done: capabilities, contracts, workflows, technology,
insights and observability are merged. This plan turns milestone 1 of
design.md into an ordered list of small PRs, each with its acceptance test,
the agent role that builds it and the skills it needs. It changes no design;
where it needs a decision it says so.

## 1. What v1 is, and is not

**v1 sees the whole fleet and changes nothing on it.** On all four machines
the owner sees every tmux pane and what runs in it, every goal and its state,
weekly usage of every subscription account with its burn-down and waste line,
GitHub issues and PRs for the watched repos, and the health of fleet-board
itself, live in the Usage, Machines and Session views and in `fleet --json`.

In v1 (capabilities tagged v1 in 01):

| Area | Capabilities | Flows |
|---|---|---|
| Connection | A1 enroll, A2 find the hub, A3 connect/snapshot/reconcile, A4 renew, A5 heartbeat, A6 wake and network change, A7 `Bye`, A8 settings | A1-A3, A5, A12 |
| Machine | B1 facts, B2 resource samples | A2 |
| Harnesses | C1 inventory and parser versions, C2 identity and sign-in, C3 folder pre-trust at setup, C4 command catalog | A6, A13 |
| Usage | D1 probe session, D2 usage reading, D3 passive signals | A6 |
| tmux | E1 inventory, E2 control-mode observer, E3 pane lifecycle, E4 activity, E5 pane summaries, E6 prompt detected (notice-only), E9 registry observation, E10 capture pane (visible screen), E11 availability, E12 harness conditions, E13 developer input | A5, A8, A9 |
| Goals and repos | G2 goal state, H1 repo inventory, H3 branch state, H4 forge CLI sign-in and push access | A8 |
| Local control | L1 local policy, L2 effective settings, M1 local audit, M2 telemetry, M3 doctor | A12 |
| Hub | GitHub sync (W4) read-only, pacing and waste projection (03b, 05), hub API reads and `Watch`, System health (05b), subscription-efficiency insights (05) | D2, D3 |

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
root, no credential reads (ADR 0011), probes type only the status command into
a fleet-owned probe session.

## 2. Definition of done

v1 is done, and `v0.1` is tagged, when all of this holds on the author's four
machines for seven consecutive days:

1. Every machine is enrolled and `ONLINE`; a hub restart and a laptop sleep
   each reconnect without loss (`EventCounters.evicted_total` stays 0).
2. For each account, the Usage view's numbers match what the harness's own
   `/usage` or `/status` screen shows at the same time, or show `UNKNOWN`,
   never a guess (checked by hand three times a day for three days).
3. The Session view lists every pane on every machine with the right harness
   and goal state within 5 s of a change.
4. Work items and change requests for the watched repos match GitHub, with
   sync lag under 5 minutes, inside the installation's rate limit.
5. The System section shows all ten signals (05b) and raised a SYSTEM triage
   item when an `always-on` machine was taken offline on purpose for over 10
   minutes (05b `AGENT_CONNECTED` counts only such machines and machines with a
   live goal).
6. `fleet --json` returns machines, accounts, goals and work items.
7. The 02e freeze checklist is ticked (generated Go and TypeScript clients
   compile in CI; mockup data checked against the protos or marked
   display-only); CI runs `buf breaking --against '.git#tag=v0.1'` from then on.

## 3. Before the first line of product code

These are cheap and unblock everything; each is its own PR.

| # | Item | Why |
|---|---|---|
| 0.1 | Fixtures of the usage and status screens for the installed versions of Claude Code, Codex and Muse, captured with the `fleet-live-probe` protocol | D2 parsers are fixture-tested (01, `fleet-harness-adapter`); harnesses auto-update (Muse moved to 1.4.2 on 2026-10-02) |
| 0.2 | Owner creates the GitHub App (the read-only permission set of ADR 0022, which adds Contents: read) and installs it on the watched repos | W4 sync needs an installation; only the owner can do it |
| 0.3 | Decide the release mechanism (plain `go build` matrix in CI vs GoReleaser) and the service manager units (launchd LaunchAgent, systemd `--user`; no root) | Packaging PR 12 depends on it; not covered by ADR 0020 |
| 0.4 | Owner sets `MUSE_NO_AUTO_UPDATE=1` on fleet machines, or accepts parser drift handled by `parsing_live` (ADR 0018) | Muse's launcher updates itself in the background (01d) |

Decisions needed from the owner are 0.2, 0.3 and 0.4; 0.1 is agent work.

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
    gateway/            AgentService: Enroll, Connect, RenewCert
    ca/                 fleet CA, enroll tokens, cert serials, revocation list
    ingest/             snapshot apply, event apply, derived state (03f)
    pacing/             usage readings, pacing, waste projection (03b, 05)
    integration/github/ GitHub adapter: typed REST wrapper + ghinstallation (ADR 0020)
    syncer/             polling loop, cursors, validators, rate limits (03j)
    api/                FleetService (Connect), Watch fan-out (02b, 02c)
    system/             System health signals and triage (05b)
    insights/           rollups (05), v1: subscription efficiency only
  agent/
    conn/               discovery, connect, backoff, epoch, outbox reader
    ring/               event ring, command log, seq persistence (agent.db, 02i)
    tmux/               control-mode observer, inventory, capture
    probe/              probe sessions on the fleet-probe server
    goals/              goal files, registry observation, repo and branch state
    policy/             local policy, settings merge, local audit
    doctor/
  harness/{claude,codex,muse,opencode}/  detect, catalog, parsers + testdata
web/                    React + TS + Vite (ADR 0007, 0020); dist embedded in fleetd
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
| 1 | **Scaffolding:** `go.mod`, `Makefile` targets (`gen`, `test`, `lint`, `gates`), `buf generate` into `gen/`, CI workflow (buf build/lint, go vet/test/race on changed packages, golangci-lint, file cap, agent canon, docs-stale), empty `cmd/` binaries that print versions | — | CI green on an empty build; generated Go and TS compile | develop | fleet-go, fleet-contract-rigor |
| 2 | **Telemetry and store foundations:** `internal/telemetry` (slog JSON, OTEL providers, no exporter by default, `/metrics` handler); `internal/store` (open with the ADR 0014 pragmas, single writer goroutine, 250 ms / 100-message batches, forward-only migrations, read pool) | 1 | writer batching and durability tests; a fullfsync check on darwin like spike S1 | develop-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 3 | **Fleet CA and enrollment (hub):** CA and `hub_identity` row, enroll tokens with CA hash, `Enroll` (retry-safe), cert serials, revocation lookup | 2 | enroll, replayed enroll, expired token, wrong CA hash refused | develop | fleet-security, fleet-go |
| 4 | **Agent connection:** `fleet-agent enroll` and `run`, discovery (cached, mDNS through `dns-sd` or `avahi`, configured: ADR 0022), mTLS connect, `Hello`/`HelloAck`, epoch, backoff with jitter, wake and network-change detection, `Bye`, `RenewCert`; `agent.db` with seq high-water mark and command log | 3 | in-memory hub fake: connect, reconnect after hub restart, seq never goes backwards, duplicate identity displaced | develop-deep | fleet-go, fleet-sqlite-store |
| 5 | **Hub gateway and ingest:** `Connect` server, snapshot apply in one tx, event apply with delivery classes, heartbeat and `last_seen` cadence (written at most once a minute: defaults.md), `EventCounters`, machine state ONLINE/STALE/OFFLINE, settings send and `SettingsAck` | 2, 4 | agent fake against real hub: snapshot then replayed events, shed counted not lost, STALE at 45 s | develop-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 6 | **Machine facts, policy, doctor:** B1, B2 (sampling per OS: ADR 0022), L1 local policy file with change detection, L2 tighten-only merge, M1 local audit, M3 `fleet-agent doctor` | 4 | policy refusals audited; doctor lists every check with a reason | develop | fleet-go, fleet-security |
| 7 | **tmux observer (epic, 2-3 PRs):** E1 inventory, E2 control mode, E3 lifecycle, E4 activity, E5 summaries (latest per pane, persisted), E10 capture (visible screen), E11 availability, E13 developer input; internal sessions marked | 5 | against real tmux 3.2 and the latest release in CI; fakes for unit tests | develop-deep | fleet-tmux, fleet-go |
| 8 | **Harness adapters and usage probes (epic):** C1-C4, D1 probe sessions on the `fleet-probe` server, D2 parsers for Claude `/usage`, Codex `/status`, Muse `/usage` with fixtures from 0.1, D3 passive signals, `VersionSupport`/`ParserSupport`/`parsing_live`, E6 prompt detection (notice-only), E12 conditions | 7, 0.1 | every fixture parses or returns `UNKNOWN`; never a guessed number; probe types only the status command | develop (parsers), develop-deep (probe session control) | fleet-harness-adapter, fleet-tmux, fleet-live-probe |
| 9 | **Goals and repos (observe only):** G2 goal state from goal files (eshu files and the generic file), E9 registry observation, H1 repo inventory and `RepoRef` derivation, H3 branch state, H4 `gh` status | 7 | eshu goal-file fixtures; a dirty checkout and a detached HEAD reported correctly | develop | fleet-go |
| 10 | **Usage, pacing and accounts (hub):** usage readings, account discovery and Codex mapping, `AccountPacing` with burn-down and `projected_unused_percent`, `reading_stale` | 5, 8 | pacing math table tests from 03b and 05; stale reading after a passed reset | develop | fleet-sqlite-store |
| 11 | **GitHub sync (read-only):** typed REST wrapper, installation tokens, conditional requests, rate-limit handling, cursors, `ProviderError`, work item and change request mirrors, trusted-gate evidence stored (not acted on), Repo resource | 2, 0.2 | recorded HTTP fixtures; 304s not counted; rate-limit back-off; sync lag metric | develop | fleet-go, fleet-security |
| 12 | **Hub API reads and Watch:** Connect handlers for machine, session, pane summary, goal, account, work item, change request, repo, triage (read), FleetSummary, GetServerInfo; `Watch` with per-topic subscriptions, resume tokens, `RESYNC_REQUIRED`, latest-wins summaries; `local_trusted` only, Host and Origin checks, loopback bind | 5, 10, 11 | contract tests per RPC; Watch resume after reconnect; slow consumer drops summaries only | develop-deep (Watch), develop (reads) | fleet-contract-rigor, fleet-security |
| 13 | **System health and insights v1:** the ten System signals, `GetSystemStatus`, SYSTEM triage items with hysteresis; subscription-efficiency rollups and `GetInsights` for them | 12 | each signal driven over its thresholds in a test; rollup equals a hand-computed week | develop | fleet-telemetry-coverage |
| 14 | **Web UI (epic):** Vite scaffold, generated client, Watch hook, then views in this order: Machines (+ System), Usage, Session (read-only panes, no attach), Triage (read-only: prompts notice-only, conditions, system), Work (read-only board), Insights (efficiency only); `web/dist` embedded in `fleetd` | 12, 13 | vitest per component; Playwright smoke per view against a hub with a seeded store; phone width | develop | fleet-frontend |
| 15 | **CLI `fleet` (read):** `fleet status`, `machines`, `accounts`, `goals`, `work`, all `--json`; `FLEET_URL`, `FLEET_TOKEN` reserved for v3 | 12 | golden JSON outputs against a seeded hub | develop | fleet-go |
| 16 | **Packaging and install:** release builds for darwin/linux amd64/arm64, launchd and systemd `--user` units, `fleetd init`, enroll-token flow, runbook in docs/ | all above, 0.3 | install, enroll and upgrade on a clean VM and on the four machines | develop | fleet-go, fleet-security |
| 17 | **v1 acceptance and `v0.1`:** the seven-day run of section 2, fixes found there, freeze checklist, tag, `buf breaking` in CI | 16 | section 2 met; review by `review-fleet-deep`; `arbiter-fleet` ruling before tagging | review-fleet-deep, arbiter-fleet | fleet-code-review |

Parallel lanes once PR 5 lands: 6, 7 and 11 are independent; 8 and 9 follow
7; 10 follows 8; 12 waits for 10 and 11; 14 can start its scaffold against
the mockup while 12 is in review.

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
  against `fleetd` with a seeded store.
- **Telemetry**: every never-dropped event kind and command kind has its
  counter (03, `fleet-telemetry-coverage`); a PR adding one without the other
  fails review.

## 7. Keeping development cost low

- **Tiers from `.agents/roles.json`:** `scan-fleet` (fast) for evidence
  gathering; `develop-fleet` and `review-fleet` (workhorse) by default;
  `-deep` only for PRs 2, 4, 5, 7, the Watch half of 12, and fixes a
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
| Laptop hub sleeps | agents back off and reconnect; the ring replays; acceptance criterion 1 tests it |
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
  and `logs.db` at the 50 GB cap (04, "Not proven").
- Cost of the weekly insights report (05, `NOT_MEASURED`).

## 10. Tracking

After this plan is approved, each row of section 5 becomes a GitHub issue
(epics for 7, 8 and 14) with its acceptance test copied in, linked to a `v1`
milestone; section 3 items become issues assigned to the owner where they need
a decision. Work then follows `fleet-issue-driver`: a worktree per issue, one
PR per change, self-review of the full diff, CI read twice before claiming
done.
