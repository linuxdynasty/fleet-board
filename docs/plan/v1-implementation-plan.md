# v1 implementation plan (watch and dispatch)

- Status: Accepted (rows 1 to 17 approved and merged 2026-10-02; rows 1b and 18 to 29 added by [ADR 0025](../adr/0025-v1-includes-dispatch.md) on 2026-10-03, for the owner to approve in its PR; tracked in the milestone "v1 — fleet": section 10)
- Date: 2026-10-02; dispatch added 2026-10-03 ([ADR 0025](../adr/0025-v1-includes-dispatch.md))
- Inputs: [design.md](../design.md), the capability map in
  [01](../specs/01-agent-capabilities.md) (v1 and v3 tags),
  [02e](../specs/02e-contract-rules.md) (freeze checklist),
  [02k](../specs/02k-v1-surface-and-cli.md) (which release serves what),
  [04](../specs/04-technology-review.md), [05](../specs/05-insights.md),
  [05b](../specs/05b-observability.md), ADRs
  [0001](../adr/0001-system-architecture.md), [0014](../adr/0014-storage-confirmed.md)
  to [0025](../adr/0025-v1-includes-dispatch.md),
  [AGENTS.md](../../AGENTS.md) and [docs/development/](../development/README.md)

The design phase is done: capabilities, contracts, workflows, technology,
insights and observability are merged. This plan turns milestone 1 of
design.md into an ordered list of small PRs, each with its acceptance test,
the agent role that builds it and the skills it needs. It changes no design;
where it needs a decision it says so. Each row is also a GitHub issue; a change
to a row changes its issue in the same PR (section 10).

## 1. What v1 is, and is not

**v1 sees the whole fleet and keeps every subscription busy.** It was two
releases, a read-only v1 and a dispatch v2; [ADR 0025](../adr/0025-v1-includes-dispatch.md)
made them one. Across all four machines the owner sees every tmux pane and what
runs in it, every goal and its state, weekly usage of every subscription account
with its burn-down and waste line, GitHub issues and PRs for the watched repos,
and the health of fleet-board itself. Then the fleet acts: it starts goals from
trusted issues on accounts with spare quota, answers prompts through approvals,
controls panes, keeps session logs, opens a terminal in the browser, and runs
routines and the weekly report on spare quota. A goal the owner starts by hand
in their own pane is an **observed goal** (decision 1 of
[ADR 0024](../adr/0024-pre-build-owner-decisions.md)): seen and reported, never
started, nudged or controlled.

**Build order.** Watching comes first (rows 1 to 16), then dispatch (rows 18 to
29), then the seven-day acceptance run (row 17) last. Row numbers are ids, not
order. `HubSettings.auto_dispatch` is `false` by default, so dispatch runs only
when the owner turns it on; manual dispatch works whenever the rows exist.

**Where v1 runs:** [02j](../specs/02j-hub-runtime.md#listeners) (the dashboard and `fleet` on the hub machine, the SSH forward, staying awake). Capabilities and their releases: [01](../specs/01-agent-capabilities.md) and [02k](../specs/02k-v1-surface-and-cli.md).

**Not in v1** (v3): sign-in and `authenticated` mode, API tokens, phone pairing
and push, remote access, the optional Tailscale lookup, and harness transcripts
(K3). "Later" items stay unscheduled (the rows marked `later` in [features.md](../features.md)).

**Security posture of v1:** [01e](../specs/01e-security-and-privacy.md) and [02g](../specs/02g-agent-security.md). The owner merges every PR of fleet-board itself (section 10).

## 2. Definition of done

v1 is done, and `v0.1` is tagged, when all of this holds on the author's four
machines for seven consecutive days. No hub-asleep time is excluded from any
item (the hub stays awake: [02j](../specs/02j-hub-runtime.md#staying-awake-and-surviving-logout)).
The dashboard and `fleet` are checked on the hub machine.

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
5. The System section lists all ten signals (05b), all measured. It raised
   a SYSTEM triage item when an `always-on` machine was taken offline on purpose
   for over 10 minutes (05b `AGENT_CONNECTED` counts only such machines and
   machines with a live goal), and `BACKUP_AGE` reads the nightly backup.
6. `fleet --json`, run on the hub machine, returns machines, accounts, goals
   (observed ones included) and work items. `fleet machine add`, `rename`, `label`,
   `revoke` and `forget` work and leave audit rows.
7. With `auto_dispatch` on for at least one watched repo, trusted issues are
   dispatched and each goal ends merged, at `READY_FOR_HUMAN_MERGE`, or in Triage
   with a reason. No issue is taken twice, no goal starts on an account inside its
   reserve or with an `UNKNOWN` or stale reading, and a goal that stops without
   `DONE` gets one nudge, then a Triage item. A goal that hits a usage limit
   resumes after the reset.
8. A prompt answered from Triage is typed once, only while that prompt still
   shows; a `CONFIRM` approval left alone is denied when `approval_timeout` passes. A drained
   machine finishes its goals and takes no new ones.
9. A stored session log replays to the same bytes, search finds a string in it,
   and a caller without `LOGS` is refused. A browser terminal attaches, types and
   detaches, and the pane keeps running.
10. The weekly report runs on Monday at 07:00 on spare quota only (never inside a
    reserve, never on an API-key account) and its report is readable on Insights.
11. The four items of the 02e freeze checklist are ticked (PR 17). `v0.1` freezes
    everything v1 implements; only v3 messages stay changeable until they ship
    (02e, ADR 0025). CI runs `buf breaking --against '.git#tag=v0.1'` from then on,
    with per-path ignores for the v3-only files.

## 3. Before the first line of product code

These are cheap and unblock everything; each is its own PR or issue. **Prove
first** (AGENTS.md, Evidence Rules): items 0.5 to 0.9 settle a theory the specs
still mark "to be confirmed", and the PR that builds on it waits for the result,
which is written into the spec the item names.

| # | Item | Why |
|---|---|---|
| 0.1 | Fixtures of the screens each parser and detector reads, for the installed versions of Claude Code, Codex, Muse, Cursor (`cursor-agent`) and OpenCode, captured with the `fleet-live-probe` protocol: usage and status screens, command catalogs, startup screens (update, release notes, trust, sign-in), prompt screens, condition screens (for example Muse "HTTP 500 retrying"), banners and status lines, and for OpenCode `providers list`, the model line and `stats` | D2 parsers, C4 catalogs, E6 and E12 detection are fixture-tested (01, `fleet-harness-adapter`); harnesses auto-update (Muse moved to 1.4.2 on 2026-10-02) |
| 0.2 | Owner creates the GitHub App (the read-only permission set of ADR 0022, which adds Contents: read) and installs it on the watched repos; `fleetd setup github` (PR 13a) writes the manifest, until then by hand. The App is the only GitHub access in v1: there is no fine-grained token fallback (decision 15 of [ADR 0024](../adr/0024-pre-build-owner-decisions.md)) | W4 sync needs an installation; only the owner can do it |
| 0.3 | **Decided** (decisions 12 to 14 of ADR 0024; issue #27 records it): binaries come from GitHub Releases with a checksum file, fetched by an install script; `fleet-agent upgrade` and `fleetd upgrade` fetch and verify the next release; no Apple signing or notarization for now (the owner clears the quarantine flag once per machine). Units: a launchd LaunchAgent on macOS (the owner is logged in; on the hub it runs `fleetd` under `caffeinate -s`); a `systemd --user` unit on Linux, with the owner running `sudo loginctl enable-linger <user>` once per worker so it survives logout (`fleet-agent doctor` checks it). Whether the release build is a plain `go build` matrix or GoReleaser is PR 16's call, inside these rules | Packaging PR 16 depends on it; not covered by ADR 0020 |
| 0.4 | Owner sets `MUSE_NO_AUTO_UPDATE=1` on fleet machines, or accepts parser drift handled by `parsing_live` (ADR 0018) | Muse's launcher updates itself in the background (01d) |
| 0.5 | **tmux probe:** does `client_activity` reach a control-mode client (push) or `refresh-client -B` subscriptions, so typing needs no 2 s `list-clients` poll; the release that introduced `pause-after` and `ignore-size` (the minimum tmux is 3.2); how 3.2 is installed on a CI runner. Run on 3.2 and the latest | PR 7 (E13, E2) builds on it; 02h E13 and 01 say "to be confirmed". Result goes into 02h and 01. If there is no push source, the 2 s `list-clients` poll stays: decision 9 of ADR 0024 allows it, and the 60 s goal-file rescan, as the two timed checks outside "poll only usage and GitHub" (ADR 0022 A.6, the streaming rule) |
| 0.6 | **Wake and network-change probe** on macOS and Linux without cgo (wall-clock jump plus interface polling, or a named library) | PR 4 (A6) builds on it; 01 says "to be confirmed in a spike" |
| 0.7 | **mDNS check:** `hashicorp/mdns` advertising from a macOS hub next to the system responder, and browsing from the agent; record the re-query interval and browse timeout in defaults.md and ADR 0023's open questions | PR 4 and PR 5 (ADR 0023); decides whether the exec fallback is ruled out |
| 0.8 | **Probe cost:** (Cursor too: [ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)) weekly percent before and after N `/usage` or `/status` probes and an idle harness start, per harness, recorded with the harness version in verified-facts.md | [03b](../specs/03b-usage-and-sync.md) says probing costs no quota; nobody measured it. If a harness spends quota, the 10-minute default becomes a per-harness number before PR 8 |
| 0.9 | **Probe folder trust (C3):** (Cursor too: [ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)) for each harness, capture the trust screen of a new folder (a fixture), and check what the harness shows on a trusted and an untrusted folder, whether trusting a parent covers new children (the weekly report's scratch folder), and the exact steps the owner follows once per harness. The agent edits no harness config in v1 (decision 3 of ADR 0024), so nothing is written or restored | 01 C3 now names the mechanism (one fleet-owned folder, trusted by hand, `FOLDER_UNTRUSTED` read from the harness screen); this item supplies the screens and the wording `fleet-agent doctor` prints |
| 0.10 | **Toolchain:** `golangci-lint` v2 built with the Go 1.27 toolchain (the installed one is built with 1.26 and refuses a `go 1.27` module), pinned in the Makefile and CI; Node 24 LTS or an accepted 22 after one `npm run build` | PR 1's gate cannot pass without it (ADR 0020) |
| 0.11 | Issue #46: probe for a usage push source. Not blocking: PR 8 ships D3 as specified; a live source lands as a follow-up PR before `v0.1` or is parked as a later item | ADR 0022 A.6 (stream where a push source exists) |
| 0.12 | Issue #57: a goal-file location every harness can write inside its sandbox without dirtying the checkout (01d) | gates row 20 (goal hooks) and row 21 (`StartGoal` confirms the goal file) |
| 0.13 | Issue #58: when Muse `SessionStart` fires, for the session-id registry fallback (01d) | gates row 20 |
| 0.14 | Issue #59: re-arm after `DONE` on Codex and Muse, and resume after a usage limit on all three harnesses (01b, 02h) | gates row 21 (goal control) and row 24 (limit wait) |
| 0.15 | Issue #60: re-run the goal-hours compression of Muse and Claude Code under the 5-minute framing, and `logs.db` at the 50 GiB cap (04, "Not proven") | gates row 25 (session logs) |
| 0.16 | Issue #61: measure the cost of the weekly insights report (05, `NOT_MEASURED`): the first run's `usage_consumed` is the measurement, recorded in 05 | gates nothing at build time; the report is on by default (ADR 0022) and its first run after row 27 lands turns `NOT_MEASURED` into a number |
| 0.17 | **Owner decision 20** (#62): in `local_trusted` mode any process on the hub machine is the owner, so a goal there could approve its own request; approvals and settings changes may need a local secret | gates row 19 (approvals and prompt answers) |
| 0.18 | **Owner decision 21** (#63): the default goal text for repos without eshu's hooks (the claim step, the `BLOCKED` convention, the review-loop cap), drafted and then approved by the owner before it goes into [03c](../specs/03c-goals.md) | gates the first `StartGoal` in such a repo (row 22) |
| 0.19 | **Cursor input line** ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)): after a turn the line still holds the old prompt and new typing is added to it; tell it apart from the fresh placeholder ("→ Plan, search, build anything") and find a safe way to clear it, every screen read before each key | gates every `SendKeys` purpose and `RunHarnessCommand` on a Cursor pane, the usage probe included (02h); result into 01b and 02h |
| 0.20 | **Cursor monthly budget:** which `/usage` bucket pacing reads (Included, Auto, API), how a monthly window is carried, and the contract change for it, made with Cursor's adapter row while the contracts are unfrozen (before row 17) | gates Cursor usage readings and dispatch; result into 03b and the usage proto |
| 0.21 | **Cursor goal control:** `/goal` syntax, pause, resume, the done signal, re-arm after done, and whether `beforeSubmitPrompt` sees `/goal` lines | gates Cursor goals; result into 01b and 01d |
| 0.22 | **Cursor account identity** from its own screens or commands, never its files | gates Cursor accounts; result into 01 C2 |
| 0.23 | **Cursor goal hooks:** which events carry G1, whether `stop` can hold a turn, how a hook finds its pane | gates Cursor goal hooks; result into 01d |
| 0.24 | **Cursor terms of use** for this kind of automation | gates auto-dispatch on Cursor; result into research/terms-of-service.md |

Items 0.1 to 0.11 come before the rows they name, most of them before PR 4.
Items 0.12 to 0.18 were the v2 prerequisites ([ADR 0025](../adr/0025-v1-includes-dispatch.md)
decision 5); each comes before the dispatch row it gates, not before PR 1. 0.16 is
measured by the report's first run and gates no build step. Items 0.19 to 0.24 are
Cursor's ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md));
each comes before the Cursor work it gates, and the other harnesses never wait for
them. Items 0.1, 0.8 and 0.9 cover Cursor too. Cursor's adapter work is row 30.

What only the owner can do is 0.2 (create and install the GitHub App, #26), 0.4
(decide Muse auto-update, #28), 0.17 (#62) and 0.18 (#63, approve the words); 0.3 is
decided (above). 0.1, 0.5 to 0.16 and 0.19 to 0.24 are agent work.
`scripts/owner-setup.sh` walks the owner through 0.2 and 0.4, the owner's one-time
folder trust that 0.9 describes (C3; provisional until 0.9 records the real screens),
and two install steps (the macOS quarantine flag, Linux linger); `--check` shows where
each stands and changes nothing.

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
    insights/           rollups, outcomes, stuck reasons, suggestions, reports (05)
    scheduler/          the planner pass, the trusted gate, slot limits, explanations (03c)
    goals/              goal state machine, dispatch checks, control, attempts (03l, 03h)
    verifier/           a DONE claim checked against GitHub before the worker is freed (03l)
    approvals/          approvals, prompt answers, the deny on timeout (03d, 02m)
    routines/           triggers, spare-quota rule, done rules, run history (03i)
    logstore/           logs.db writer, retention, search, the last-screen archive (03d, ADR 0015)
    attach/             attach tickets and the WebSocket to Terminal bridge (02f, 03d)
  seed/                 test seed: a recorded agent stream replayed into a real hub store (PR 12)
  agent/
    conn/               discovery, connect, backoff, epoch, outbox reader
    ring/               event ring, command log, seq persistence (agent.db, 02i)
    tmux/               control-mode observer, inventory, capture
    probe/              probe sessions on the fleet-probe server
    goals/              goal files, registry observation, repo and branch state
    policy/             local policy, settings merge, local audit
    exec/               command executor: ack, progress, result, dedupe, rate limit (02e, 02i)
    control/            pane commands behind the 02h checks, local approval (01f F, L3)
    start/              Preflight and the StartGoal steps, goal control through /goal (03c, 02h)
    worktree/           worktrees and scratch folders (H2, F7)
    hooks/              install and verify goal hooks per harness, hook events (01d)
    logship/            pipe-pane capture, zstd frames, spool, shipping (K1, K2)
    attach/             grouped view session and PTY for one attach (J1)
    doctor/
  harness/{claude,codex,muse,cursor,opencode}/  detect, catalog, parsers + testdata
web/                    React + TS + Vite (ADR 0007, 0020); dist embedded in fleetd
hooks/                  the one Codex-format goal-hook definition and the Muse plugin package (ADR 0001, 01d)
```

Every Go package carries `doc.go`, `README.md` and a scoped `AGENTS.md`
(AGENTS.md, Documentation Discipline).

## 5. The PR sequence

Each row is one PR (or a small epic of PRs where noted), ordered by
dependency. Row numbers are ids, not order: 2b, 13a and 1b were added later, and
row 17 (acceptance) runs last, after the dispatch rows 18 to 29 (ADR 0025) and
row 30, the Cursor adapter (ADR 0027). **Role** is the cheapest tier that fits (`.agents/roles.json`);
the orchestrator escalates to `-deep` only for the reasons in AGENTS.md.
Every PR follows AGENTS.md: failing test first for bugs, contract proof for
new code, `fleet-code-review` before push, the full local gate once.

| PR | Scope | Depends on | Acceptance test | Role | Skills |
|---|---|---|---|---|---|
| 1 | **Scaffolding:** `go.mod`, `Makefile` targets (`gen`, `test`, `lint`, `gates`; `gen/` is never committed, so every gate and the worktree recipe start with `make gen`, and `make gates` prints the tool versions it used), `buf generate` into `gen/`, CI workflow (buf build/lint, go vet/test/race on changed packages, golangci-lint, `scripts/check-file-cap.sh`, agent canon with its attribution check on commits and the PR body, docs-stale as an advisory step), empty `cmd/` binaries that print versions; a client-side guard, `.githooks/pre-push` wired by `core.hooksPath`, that refuses a push to `main` and runs the local gate (decision 2 of ADR 0024: the repo is private on a plan without branch protection, and the owner merges every PR). Files at the cap (02-contracts.md, 02b, 01, 03c, status.proto) are split in the first PR that has to add to them | 0.10 | CI green on an empty build; generated Go and TS compile; a seeded 500-line file and a model `Co-Authored-By` trailer each fail CI; with `core.hooksPath` set, a push to `main` is refused and a push of a branch runs the gate | develop-fleet | fleet-go, fleet-contract-rigor |
| 1b | **Drift checks** ([ADR 0025](../adr/0025-v1-includes-dispatch.md) decision 7): a CI check that every backticked proto name in `docs/` exists in `api/fleet/`; the docs-stale step becomes blocking: a PR that changes a mapped code folder changes its spec (for example `internal/agent/tmux` to 02h and 01 E) or carries a `No-Doc-Change: <reason>` line, like `No-Observability-Change:`; the generated tables of [ADR 0026](../adr/0026-one-home-per-fact.md) decision 5, each written from proto comments by a script and marked "to be generated" until this row lands: the calls and Watch topics by release (02k, from the release comment on each call and topic), the scope tables (02d, from the scope comment on each call), the delivery classes (02i, from the delivery class on each event), and the capability-to-message map (02, from the capability ids on messages); a report of each v1 capability id of 01 that no test names (a report until row 17) | 1 | a seeded unknown name in a doc fails CI and passes once removed; a seeded code change with no spec change fails, and passes with the trailer; each of the four generated tables (02k, 02d, 02i, 02) equals the committed one, a seeded change to a proto comment changes its table, and a hand edit of any of them fails CI | develop-fleet | fleet-contract-rigor, fleet-code-review |
| 2 | **Telemetry and store foundations:** `internal/telemetry` (slog JSON, OTEL providers, no exporter by default, `/metrics` handler); `internal/store` (open with the ADR 0014 pragmas, single writer goroutine, 250 ms / 100-message batches, forward-only migrations, read pool) | 1 | writer batching and durability tests; a fullfsync check on darwin like spike S1 | develop-fleet-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 2b | **`fleetd` shell:** `fleetd.yaml` load and validation with the 02j defaults, `fleetd init` (config and state directory), the agent TLS listener with the server certificate and its SANs (`tls.agent_names`), `admin.sock` (mode 0600) with `fleetd enroll-token`, `fleetd run` | 2 | a fresh `fleetd init` then `fleetd run` starts, refuses a non-loopback API address in `local_trusted`, and `fleetd enroll-token` prints a token | develop-fleet | fleet-go, fleet-security |
| 3 | **Fleet CA and enrollment (hub):** CA and `hub_identity` row, enroll tokens with CA hash, `Enroll` (retry-safe), cert serials, revocation lookup and the `revoked-serials` file; the nightly `VACUUM INTO` backup into `backups/` (mode 0600; keep the last 7, made at 03:00 hub local time: [defaults](../specs/defaults.md), decision 16 of ADR 0024) and `fleetd backup [--encrypt]` (03f: a backup holds the CA key; `--encrypt` uses `age`), which is what `BACKUP_AGE` reads | 2b | enroll, replayed enroll, expired token, wrong CA hash refused; a backup opens, holds `hub_identity`, and is mode 0600 | develop-fleet | fleet-security, fleet-go |
| 4 | **Agent connection:** `fleet-agent enroll` and `run`, discovery (cached, mDNS through `hashicorp/mdns`, configured: ADR 0023), mTLS connect, `Hello`/`HelloAck`, epoch, backoff with jitter, wake and network-change detection (0.6), `Bye`, `RenewCert`; `agent.db` with seq high-water mark and command log; a minimal facts stub in `Hello` until PR 6 | 3, 0.6, 0.7 | in-memory hub fake: connect, reconnect after hub restart, seq never goes backwards, duplicate identity displaced; advertise and discover on a macOS hub with the system responder running, re-query interval and browse timeout recorded in defaults.md | develop-fleet-deep | fleet-go, fleet-sqlite-store |
| 5 | **Hub gateway and ingest:** `Connect` server, snapshot apply in one tx, event apply with delivery classes, heartbeat and `last_seen` cadence (written at most once a minute: defaults.md), `EventCounters`, machine state ONLINE/STALE/OFFLINE, mDNS advertise on the agent port; **command path** (`internal/hub/outbox`): outbox and `command_results` tables, send with epoch and seq, `CommandAck`/`CommandProgress`/`CommandResult`/`ResultAck` apply, `not_after_time` expiry, `SnapshotRequest`, `Operation` store; settings send and `SettingsAck`; the writer stamps `hub_seq`, `version`, `observed_time` and writes `audit_log` rows; **triage** raise and retire in the event transaction for the v1 kinds (prompt, condition, parser_degraded, usage_parse_failed, parser_unsupported, policy_invalid, policy_changed, hostname_mismatch, duplicate_identity, settings_unenforced; PR 10 raises `pacing` and PR 11 `integration_access_lost` with the same code, PR 13 the `system` items; the dispatch kinds are raised by the rows that build their source) with the 03d fingerprint and 7-day expiry; the housekeeping job (STALE and OFFLINE marking, retention per table, outbox expiry: 03f); `fleetd restore` with the 24 h reconcile flag (03f, 02g) | 2, 3, 4 | agent fake against real hub: snapshot then replayed events, shed counted not lost, STALE at 45 s, an expired command ends `EXPIRED`, a duplicate `command_id` runs once; a seeded condition raises one deduplicated triage item and retires it; each retention table prunes in a test; after a restore agents reconnect with the pinned CA and the hub adopts what they know | develop-fleet-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 6 | **Machine facts, policy, command executor, doctor:** B1, B2 (sampling with gopsutil: ADR 0023), L1 local policy file with change detection (every key, type and default is in 03e; a missing file is a normal first run, and `fleet-agent enroll` asks for the repos folder and writes a starter `policy.yaml`), L2 tighten-only merge, M1 local audit, M3 `fleet-agent doctor` (including the probe-folder trust check per harness and, on Linux, the linger check), C3 (the probe folder, 01); the agent **command executor** (`CommandAck`/`CommandProgress`/`CommandResult`, deadline from `HelloAck.hub_time`, duplicate `command_id` returns the logged result, an unknown kind answers `UNSUPPORTED`, per-kind rate limit) | 4 | policy refusals audited; a fresh agent with no `policy.yaml` starts, reports an empty `allowed_repo_roots` and allows every installed harness; `enroll` writes the starter file; doctor lists every check with a reason; a replayed command returns its logged result; the sampler checked on the four machines | develop-fleet | fleet-go, fleet-security |
| 7 | **tmux observer (epic, 2-3 PRs):** E1 inventory, E2 control mode, E3 lifecycle, E4 activity, E5 summaries (latest per pane, persisted), E10 capture (visible screen), E11 availability (idle reporting; the owner's mark is row 18), E13 developer input as 0.5 decides; internal sessions marked | 5, 0.5 | against real tmux 3.2 and the latest release in CI (3.2 installed as 0.5 found); `pause-after` and `ignore-size` gated by version; fakes for unit tests | develop-fleet-deep | fleet-tmux, fleet-go |
| 8 | **Harness adapters and usage probes (epic):** C1-C4, D1 probe sessions on the `fleet-probe` server, D2 parsers for Claude `/usage`, Codex `/status`, Muse `/usage` with fixtures from 0.1, D3 passive signals, `VersionSupport`/`ParserSupport`/`parsing_live`, E6 prompt detection (answers are row 19), E12 conditions; the OpenCode adapter (inventory, `providers list` for account discovery, model line, spend from `stats`; no usage window, no `/goal`); the probe runs in the fleet-owned probe folder (C3; the agent edits no harness config); a probe pane on a startup screen (trust, update, login, release notes) is `alive=false`, `STARTUP_PROMPT_SEEN`, no key typed, failover tried, and one triage item says how to answer it; a trust screen is reported as `FOLDER_UNTRUSTED` from the harness's own screen | 7, 0.1, 0.8, 0.9 | every fixture parses or returns `UNKNOWN`; never a guessed number; probe types only the status command; the Codex update-screen fixture yields the startup-screen failure and no key press; a fixture of each harness's trust screen yields `FOLDER_UNTRUSTED` and no key press; `fleet-agent doctor` run on that screen prints the one-time step for the owner | develop-fleet (parsers), develop-fleet-deep (probe session control) | fleet-harness-adapter, fleet-tmux, fleet-live-probe |
| 9 | **Goals and repos (observe):** G2 goal state from eshu goal files (`.claude/active-goal.<session_id>`; the generic file is row 20), E9 **observed goals**: a goal file found by the pane's working folder and the newest goal file ([02h](../specs/02h-goal-binding-and-pane-signals.md#observed-goals-v1)), reported with binding source `OBSERVED` and an agent-minted id, never dispatched, nudged or controlled, ended by `DONE` or pane exit; the 60 s goal-file rescan; H1 repo inventory and `RepoRef` derivation, H3 branch state, H4 `gh` status | 7 | eshu goal-file fixtures; a goal file in a pane's folder yields one observed goal with the right state, a second harness pane in the same folder yields none (ambiguous, never guessed), `DONE` and pane exit each end it, a restart of the agent keeps its id, and no command or nudge is ever sent to it; a dirty checkout and a detached HEAD reported correctly | develop-fleet | fleet-go |
| 10 | **Usage, pacing, probes and accounts (hub):** usage readings, account discovery and Codex mapping, `AccountPacing` with burn-down and `projected_unused_percent`, which is unset until 24 hours of the usage window have passed (worked examples and edge cases in 03b); API-key accounts show "spent so far" as the tool reports it, with no cap period, `reading_stale`, `insight_pacing_interval` rows on each change; **probe assignment** (`internal/hub/pacing/probe`): the `probes` table with its unique-active constraint, candidate rules and preference, failover on OFFLINE or 3 failures, `StopProbe` reconcile from the snapshot, the passive-reading `run_now` trigger, a 30 s tick | 5, 8 | pacing math table tests from 03b and 05, including no projection before hour 24 of a window; stale reading after a passed reset; two passes never assign one account twice; failover on OFFLINE and on 3 failures; `run_now` at most once a minute per account | develop-fleet | fleet-sqlite-store |
| 11 | **GitHub sync (read-only):** REST for issues with ETags, GraphQL for change requests and timeline evidence (batched), installation tokens, conditional requests, rate-limit handling, cursors, `ProviderError`, work item and change request mirrors, the 5-minute claim loop, the trust verdict and stage computed in the same transaction as the item upsert (stored here; the planner of row 23 enforces it), change request found by the agent-reported `BranchState.branch`, Repo resource, and the `Integration` row created from the `github:` keys of `fleetd.yaml`. GitHub App access only: there is no fine-grained token fallback in v1 (decision 15 of ADR 0024) | 2b, 0.2 | recorded HTTP fixtures; 304s not counted; rate-limit back-off; sync lag metric; a trusted and an untrusted item get the right stage | develop-fleet | fleet-go, fleet-security |
| 12 | **Hub API reads and Watch:** the v1 read set of [02k](../specs/02k-v1-surface-and-cli.md) (machine, session, `CapturePane`, goal and runs, work item, change request, repo, account, usage readings, triage, integrations, settings, audit entries, store status, `GetOperation`, `FleetSummary`, `GetServerInfo`); filters with the 02c field catalogue, paging, the change log, coalescing and per-principal rate limits; `Watch` with per-topic subscriptions, resume tokens, `RESYNC_REQUIRED`, latest-wins summaries; every RPC and topic not built yet answers `UNIMPLEMENTED` or is refused (02k), and each dispatch row turns its own on; the **seed package** (`internal/seed`); a placeholder `web/dist/index.html` so `go build` embeds; `local_trusted` only, Host and Origin checks, loopback bind | 5, 10, 11 | contract tests per RPC; a filter, a page and a stale `RESYNC_REQUIRED` token tested per resource; Watch resume after reconnect; slow consumer drops summaries only; a v3 RPC returns `UNIMPLEMENTED` | develop-fleet-deep (Watch), develop-fleet (reads) | fleet-contract-rigor, fleet-security |
| 13 | **System health and efficiency insights:** the ten System signals, `GetSystemStatus`, SYSTEM triage items with hysteresis; subscription-efficiency rollups and `GetInsights` for them; `fleetd insights rebuild` | 12 | each signal driven over its thresholds in a test (`LOG_STORE_USAGE` with an injected value; it reads `UNKNOWN` until row 25 adds `logs.db`); rollup equals a hand-computed week | develop-fleet | fleet-telemetry-coverage |
| 13a | **Owner configuration:** the write RPCs of [02j](../specs/02j-hub-runtime.md#configuring-a-fresh-hub) behind `SETTINGS` and `request_id`/`expected_version`: `CreateRepo`/`UpdateRepo`/`DeleteRepo` (watched repos, caps, consent), `CreateAccount`/`UpdateAccount`/`DeleteAccount` (`reserve_percent`, default 10; Codex `bindings`; a new account starts with consent push, pr-open, merge), `UpdateIntegration` (label, tracker projects), `UpdateSettings`, `ExportConfig`/`ImportConfig` with `plan_hash`, `SnoozeTriageItem`/`DismissTriageItem` (hub state only); the idempotency store and update-mask validation; every write audited; `fleetd setup github` (steps in 02j: it writes the App manifest and the `github:` keys); and **machine admin**, confirmed for v1 (decision 5 of ADR 0024): `CreateEnrollmentToken` (the "add machine" command with its token), `UpdateMachine` (rename, labels), `RevokeMachine`, `DeleteMachine` (forget) | 10, 11, 12 | a fresh hub is configured only through these calls (no SQL, no hand-edited database; the dashboard forms are PR 14, the CLI is PR 15): watch a repo, set a reserve, map a Codex account, import a `FleetConfig` plan (dry run, then apply, then a stale `plan_hash` refused) and apply it; a stale `expected_version` is refused; an enrollment token is created and an agent enrolls with it, a machine is renamed and labelled, a revoked machine's certificate is refused at its next handshake, and `DeleteMachine` is refused until the machine is revoked; every write leaves an audit row; the Watch topics `REPOS`, `ACCOUNTS`, `SETTINGS` show each change; `setup github` runs against recorded GitHub responses and writes its key file mode 0600 | develop-fleet | fleet-contract-rigor, fleet-security, fleet-go |
| 14 | **Web UI (epic):** Vite scaffold, generated client, Watch hook, then views in this order: Machines (+ System), Usage, Session (read-only panes, no attach: the last lines of each pane from `PaneSummary.last_lines`, plus the visible screen on demand with `CapturePane`), Triage (prompts, conditions, system; snooze and dismiss; answers and approvals are row 28), Work (read-only board), Insights (efficiency only), Settings (the forms for the writes of PR 13a); `web/dist` embedded in `fleetd`; the router and styling choice gets its ADR 0020 table line first; the mockup is marked display-only, its dispatch controls are row 28 and its v3 controls are listed (ui-contract-map) | 12, 13, 13a | vitest per component; Playwright smoke per view against `fleetd` started on the seed; phone width; each view shows only what the built rows serve | develop-fleet | fleet-frontend |
| 15 | **CLI `fleet` (read, and the writes of 13a):** `fleet status`, `machines`, `accounts`, `goals`, `work`, `doctor`, all `--json`; `fleet repo`, `fleet account`, `fleet settings`, `fleet machine` (add, rename, label, revoke, forget), `fleet export`, `fleet import --dry-run` (the `fleet.yaml` renderer and parser: the canonical JSON of `FleetConfig` written as YAML, so enum names are in full, with `format_version: 1`, as [config.proto](../../api/fleet/hub/v1/config.proto) defines); `FLEET_URL`; `FLEET_TOKEN` optional in `local_trusted` (02f). Commands and release tags: [02k](../specs/02k-v1-surface-and-cli.md) | 12, 13a | golden JSON outputs against the seed; the configuration walk of 13a repeated through the CLI; export then import of the same hub is an empty plan | develop-fleet | fleet-go |
| 16 | **Packaging and install:** release builds for darwin/linux amd64/arm64 published to GitHub Releases with a checksum file; the install script (verifies the checksum before it installs); `fleet-agent upgrade` and `fleetd upgrade` (fetch the next release, verify it, swap the binary, restart the service; `agent.db` and `state.db` migrate forward only); launchd LaunchAgent units, the hub's running `fleetd` under `caffeinate -s`; `systemd --user` units with the one-time `sudo loginctl enable-linger <user>` step on Linux workers; no Apple signing or notarization (the runbook gives the one command that clears the quarantine flag); the documented `fleetd init` and `fleetd enroll-token` (`claim` is v3); runbook in docs/, including the SSH forward for using the dashboard and `fleet` from another machine ([02j](../specs/02j-hub-runtime.md#listeners)) | all above, 0.3 | install, enroll and upgrade on a clean VM and on the four machines; a tampered archive fails the checksum and installs nothing; a Linux worker still reports `ONLINE` after the owner logs out; the hub stays awake under `caffeinate -s` (a sleep test shows it) | develop-fleet | fleet-go, fleet-security |
| 18 | **Agent pane control:** F1 `EnsureSession`, F2 `EnsureWindow`, F3 `SendKeys` with the checks for each `SendKeys.purpose` (a prompt answer needs that `prompt_id` still showing; a nudge, resume or operator text needs the ready prompt and an empty input line; nothing while the developer types), F4 `Interrupt`, F5 `RespawnPane`, F6 `KillWindow`, F8 `RunHarnessCommand` (the allow-listed catalog and command classes of 01b), `SetPaneAvailability` (the owner's mark, E11), E7 ready level, B3 power state; the role and command matrix of 02g, per-kind policy and rate limits; L3 local approval (`fleet-agent approve`, `deny`) | 6, 7, 8 | real tmux in CI: each kind refused with its reason when its check fails (prompt gone, input line not empty, developer typing) and no key typed; a destructive prompt defaults to No; a `DESTRUCTIVE` kind under local approval waits for `fleet-agent approve`; a replayed `command_id` runs once | develop-fleet-deep | fleet-tmux, fleet-security, fleet-harness-adapter |
| 19 | **Approvals and session control (hub):** the approvals store and `ListApprovals`/`GetApproval`/`DecideApproval` with the deny on timeout (`approval_timeout` in [defaults](../specs/defaults.md)), the `APPROVALS` Watch topic; the hub calls `CreateSession`, `SendKeys`, `InterruptPane`, `RunHarnessCommand`, `SetPaneAvailability`, `RespawnPane`, `KillWindow`, each audited and routed through an approval where its class needs one (02m, 03d); the prompt-answer flow ([03d](../specs/03d-triage-sessions-logs.md#w7-triage-items)): one answer in flight, typed only while that prompt shows; owner decision 20 as 0.17 settles it | 12, 18, 0.17 | a `CONFIRM` action waits and is denied when `approval_timeout` passes; an answer to a prompt that has gone fails with no keys typed; a second answer while one is in flight is refused; each write leaves an audit row and a Watch change | develop-fleet | fleet-security, fleet-contract-rigor |
| 20 | **Worktrees, scratch folders and goal hooks:** H2 `EnsureWorktree`/`PruneWorktree`, F7 `EnsureScratch`/`CollectScratch`, G1 `EnsureGoalHooks` (Claude Code hooks, the one Codex-format definition and the Muse plugin package in `hooks/`), E8 hook events, the generic goal-file location (0.12) and the session-id registry fallback (0.13), `RegisterGoal`/`ReleaseGoal`; the hub's `EnsureMachineHooks` (the Machines view part is row 28); goal hooks reconcile (03h) | 9, 12, 18, 0.12, 0.13 | a worktree is made and pruned without touching the developer's checkout; hooks install and verify per harness against fixtures, and uninstall cleanly; a turn-end hook event arrives as E8; a scratch folder is collected | develop-fleet | fleet-harness-adapter, fleet-security, fleet-go |
| 21 | **Preflight, `StartGoal` and goal control (agent):** I1 `Preflight` with named reasons (`HARNESS_NOT_SIGNED_IN`, `PUSH_ACCESS_MISSING`, `CHECKOUT_MISSING`, `FOLDER_UNTRUSTED`, `DISK_LOW`); G3 `StartGoal` in its 14 steps (02h, 03c): reuse a free pane only in the same repo with a clean checkout (a new branch in place), or a new window in a fresh worktree, `fresh_context_on_reuse` (off by default), model and effort, the registry written first, `/goal` typed, the goal file confirmed; G4 goal control (pause, resume, edit, clear) through the harness's own `/goal` by `RunHarnessCommand`; re-arm after `DONE` and resume after a usage limit as 0.14 found; K1 `pipe-pane` capture of the goal pane into the agent's local raw files (`StartGoal` step 7, 02h; plain append files, 04 Q9), with shipping, `logs.db` and 0.15 left to row 25 | 20, 0.14 | each preflight failure returns its reason and types nothing; a replayed `StartGoal` runs once; a goal file not seen in time fails the start with a reason; pause, resume and re-arm against each harness's fixtures | develop-fleet-deep | fleet-tmux, fleet-harness-adapter, fleet-security |
| 22 | **Goals and the verifier (hub):** `PreflightGoal`, `StartGoal`, `ControlGoal`; the goal state machine and `GoalPhase` classes (03l); dispatch checks with Block, Confirm, Warn and Info (03h); attempts (every retry is a new goal); the verifier: a `DONE` claim is checked against GitHub before the worker is freed, a mismatch is `disagree` in Triage; `READY_FOR_HUMAN_MERGE` when the consent has no merge; the work item stage after a goal ends; dispatched goals in the `ListGoalRuns` and `ListGoalTransitions` of row 12; the default goal text for repos without eshu's hooks (0.18) | 11, 12, 19, 21, 0.18 | a manual dispatch end to end against the agent fake and recorded GitHub; a `CONFIRM` check returns an approval and starts nothing until it is decided; a `DONE` that GitHub contradicts becomes `disagree`; a goal without merge consent stops at `READY_FOR_HUMAN_MERGE`; every transition audited and on Watch | develop-fleet-deep | fleet-sqlite-store, fleet-contract-rigor |
| 23 | **The planner (auto-dispatch):** the 30 s pass of 03c: `READY` items only, the trusted gate enforced, most waste first, paced against the burn-down line, base cap of 2 active goals per account, the per-repo cap (default 3), two slot limits per machine, `prefer_non_hub`, the reserve, no dispatch on an `UNKNOWN` or stale reading; the claim re-check (W5) and the `double_claim` item; `auto_dispatch` off by default; scheduler explanations (03h); `RequeueWorkItem` and manual-only after three failures; no catch-up flood after an outage; `ProbeAccount` | 10, 22 | table tests of one planner pass from 03c; no goal starts inside a reserve or on a stale reading; two passes never start two goals for one item; after a hub outage dispatch resumes at the normal pace | develop-fleet-deep | fleet-sqlite-store, fleet-telemetry-coverage |
| 24 | **Reliability rules and drain:** one corrective nudge, the escalating cooldown, the review-loop cap (3 rounds), stuck and verifying fingerprints, limit wait (up to a week, resumed at the reset, 0.14), failed starts (durable refusals mark a machine unfit, re-checked on a schedule), provider-side changes under a live goal (03h); M4 drain and undrain (`DrainMachine`, `UndrainMachine`, `fleet-agent drain`, `undrain`); nothing is nudged while a person types, a prompt shows or a limit shows | 18, 22, 23 | each rule driven by a fake clock: one nudge then Triage; the cooldown sequence; a pause after 3 review rounds; a limit wait resumes at the reset; a drained machine finishes its goals and gets no new one | develop-fleet-deep | fleet-tmux, fleet-sqlite-store |
| 25 | **Session logs:** K1 shipping of the raw capture of row 21 (and of panes the owner turns capture on for), zstd frames (5 min or 4 MiB, flushed every 10 s), `SetLogCapture`, `SetLogShipping`; K2 the agent spool, low-disk pause and resume; the `SessionLog` stream with `LogAck`; the hub's `logs.db` with its own writer, retention and the 50 GiB cap (0.15); `ReadSessionLog`, `ListStoredSessions`, `GetStoredSession`, `DeleteSessionLogs`, `SearchSessionLogs`, `GetLogStoreStatus`, all behind `LOGS`; the last pane summary archived when its session ends (ADR 0015, ADR 0025); `fleet logs export`; `LOG_STORE_USAGE` measured | 5, 7, 12, 21, 0.15 | replay equals the captured bytes; the spool survives the hub being away and drains; the cap prunes the oldest; one session deleted in one step; a caller without `LOGS` is refused | develop-fleet-deep | fleet-sqlite-store, fleet-security, fleet-performance-rigor |
| 26 | **Terminal attach:** J1 `OpenAttach`/`CloseAttach` (a grouped view session and a PTY), the `Terminal` stream, `CreateAttachTicket` (one use, short-lived), `AttachTerminal` over a WebSocket, the attach bridge with flow control and resize, also in `local_trusted` (02f) | 12, 18 | a ticket works once and expires; keystrokes round-trip; a slow browser never delays commands on `Connect`; closing the tab leaves the pane running | develop-fleet-deep | fleet-tmux, fleet-security |
| 27 | **Routines, the weekly report and the rest of Insights:** routines with a cron or empty-queue trigger, the spare-quota rule (behind pace or 5 points unused, never into the reserve, never on an API-key account), coalesce and skip, done rules, run history; `ListRoutines` to `RunRoutine` and the `ROUTINES` topic; the built-in weekly report (Monday 07:00, on by default, its cost measured on its first run, 0.16), `InsightReport`, suggestions with evidence, the outcomes and stuck sections of `GetInsights` | 22, 23 | a routine never starts inside a reserve or on an API-key account; catch-up after an outage is skipped; the report ends `REPORT_WRITTEN`; each suggestion carries its evidence; the outcome rollups equal a hand-computed week | develop-fleet | fleet-sqlite-store, fleet-telemetry-coverage |
| 28 | **Web UI, dispatch (epic):** dispatch with the preflight dialog (Block, Confirm, Warn, Info), goal control, approvals and prompt answers in Triage, the `a` key, the owner's pane mark, session control, the browser terminal (xterm.js), log replay and search, routines, reports and suggestions, drain, hook approval on Machines | 14, 19, 22 to 27 | Playwright smoke per flow on the seed; phone width; a control the caller's scope does not allow is hidden | develop-fleet | fleet-frontend |
| 29 | **CLI `fleet`, dispatch:** `fleet dispatch`, goal control, `fleet requeue`, `fleet routine` (`fleet logs export` is row 25; [02k](../specs/02k-v1-surface-and-cli.md)), with `--json` | 15, 22 to 27 | golden JSON outputs against the seed; each command refused with the API's reason when its check fails | develop-fleet | fleet-go |
| 30 | **Cursor adapter (epic)** ([ADR 0027](../adr/0027-cursor-first-class-copilot-and-messaging-later.md)): the `cursor` adapter for `cursor-agent` in a tmux pane: C1 inventory, C2 identity as 0.22 found, C4 command catalog, the trust box reported as `FOLDER_UNTRUSTED`, E6 prompt detection under each approval mode, the input-line rule of 0.19 (until it is proven, every `SendKeys` purpose and `RunHarnessCommand` answers `PANE_BUSY` on a Cursor pane, the probe included), the D2 `/usage` parser and the monthly budget of 0.20 with its contract change (before row 17), G1 goal hooks as 0.23 found, G3 and G4 goal start and control as 0.21 found, resume with `cursor-agent --resume`; `scripts/owner-setup.sh` and the mockup data name Cursor; auto-dispatch on Cursor only once 0.24 allows it | 8, 20, 21, 0.19, 0.20, 0.21, 0.22, 0.23, 0.24 | every Cursor fixture parses or returns `UNKNOWN`; a pane showing old input text or the placeholder is never typed into except by the rule 0.19 proved, tested on fixtures of both; a `/usage` fixture yields a monthly bucket that pacing reads as 0.20 decided; Cursor hooks install, verify and uninstall against fixtures; pause, resume and re-arm against Cursor's fixtures | develop-fleet (parsers), develop-fleet-deep (typing gate, goal control) | fleet-harness-adapter, fleet-tmux, fleet-contract-rigor, fleet-live-probe |
| 17 | **v1 acceptance and `v0.1` (runs last):** the seven-day run of section 2, watching and dispatch, fixes found there, the four items of the 02e freeze checklist (every contract-review finding fixed or deferred; generated clients compile in CI; the mockup data checked against the protos or marked display-only; `go.mod`, the generated-code location and the CI job), tag `v0.1` for everything v1 implements, with per-path `breaking` ignores in `buf.yaml` for the v3-only messages (02e, ADR 0025); the capability-to-test report of row 1b fails CI from here, `buf breaking` in CI | 16, 18 to 29 | section 2 met; review by `review-fleet-deep`; `arbiter-fleet` ruling before tagging | review-fleet-deep, arbiter-fleet | fleet-code-review |

Parallel lanes: after 2b, PR 3 and PR 11 are independent; after PR 5, 6 and 7
are independent of each other; 8 and 9 follow 7; 10 follows 8; 12 waits for 10
and 11; 13a follows 10, 11 and 12; 14 can start its scaffold against the mockup
while 12 is in review. 1b follows 1 at once. Dispatch: 18 follows 6, 7 and 8; 19,
20 and 26 follow 18; 21 follows 20; 22 follows 21 and 19 (and 11, 12), so no goal starts before owner decision 20 (0.17); 23 and 24 follow
22; 25 follows 21 and 12; 27 follows 23; 28 and 29 come
last, before 17. Row 30 (Cursor) follows 8, 20 and 21 and its items 0.19 to 0.24;
the other rows never wait for it. The 0.1 to 0.11 probes run first and in parallel; 0.12 to 0.18
run before the rows they gate.

## 6. How it is tested

- **Fakes at every edge:** tmux, harness screens, clock,
  network, power, file watching. Hub and agent are tested against an
  in-memory fake of each other over a real gRPC connection, so no `sshd`, keys
  or machines are needed for CI.
- **Parser fixtures per harness version** under `internal/harness/*/testdata/`;
  a new harness version without fixtures is `UNVERIFIED_PARSING`, never assumed.
  The first fixtures are the real `/status` and `/usage` captures from
  2026-09-28 and 29. Versions move fast (Codex went 0.157.1 to 0.159.2 and Muse
  1.4.0 to 1.4.1 in two days), so fixture capture is a scripted job
  (`fleet-agent capture-fixtures`), not a manual step. Fixtures are scrubbed of
  emails, names and installed skill names before they are committed.
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

- **Tiers:** the Role column is the cheapest tier that fits; escalation rules are in AGENTS.md.
- **Small PRs** (one row or sub-row of section 5): a failed attempt costs one PR, not a milestone.
- **Specs are the context:** a handoff names the spec sections and protos a PR implements. Gates run once, by the orchestrator (AGENTS.md).
- **fleet-board drives itself** as soon as rows 22 and 23 land: its own issues
  become goals on spare quota (the owner still merges every PR).

## 8. Risks and how v1 handles them

| Risk | Handling |
|---|---|
| A harness auto-updates and its screens change | `parsing_live` keeps dispatch-relevant parsing going (ADR 0018); fixtures are refreshed with the probe protocol; `UNKNOWN` is shown rather than a guess |
| Provider terms on automation ([terms-of-service.md](../research/terms-of-service.md)) | each harness runs unmodified, signed in by the owner, in tmux sessions the owner can see; no headless use, no credential reads; the hub never writes to GitHub; written confirmation from providers is still needed before offering fleet-board to others (design.md) |
| Laptop hub sleeps | the hub's launchd unit runs `fleetd` under `caffeinate -s`, so it stays awake while it runs (PR 16, decision 12 of ADR 0024); if it still stops, agents back off and reconnect and the ring replays; acceptance criterion 1 tests it |
| tmux versions differ (3.2 to 3.7 on the fleet) | CI runs both ends; control-mode features gated by version |
| GitHub rate limits | conditional requests and the 26 % budget measured in 04; sync lag is a System signal |
| Store durability on a laptop | `fullfsync` on darwin, proved in spike S1 and re-checked in PR 2 |
| A wrong usage reading now spends quota | `UNKNOWN` and stale readings never dispatch; the reserve holds; `auto_dispatch` is off by default; the parsers are proven by the watch rows before any dispatch row lands (ADR 0025) |
| A typed key lands in the wrong place | every pane-changing kind passes the checks for its `SendKeys.purpose` (02h) and the local policy; nothing is typed while the developer types; destructive prompts default to No (row 18) |
| v1 is twice as large | rows stay small and ordered; watching works on its own before dispatch starts, and `auto_dispatch` stays off until the owner turns it on |

## 9. Open owner decisions

Open: items 0.17 (#62) and 0.18 (#63) in section 3 block dispatch; #64 (phone push) waits for v3.

## 10. Tracking

Each row of section 5 is a GitHub issue (epics for 7, 8, 14, 28 and 30) with its
acceptance test copied in, in the milestone "v1 — fleet". This table maps plan
ids to issues; `scripts/plan-issues.py` reads it.

| Plan ids | Issues |
|---|---|
| 1 to 17 (the numbered rows, in order) | #29 to #45 |
| 2b | #56 |
| 13a | #48 |
| 1b | #66 |
| 18 to 29 | #67 to #78 |
| 0.1 to 0.4 | #25 to #28 |
| 0.5 to 0.10 | #50 to #55 |
| 0.11 | #46 |
| 0.12 to 0.18 | #57 to #63 |
| 30 | #95 |
| 0.19 to 0.24 | #96 to #101 |

Section 3 items are issues assigned to the owner where they need a decision. Work
follows `fleet-issue-driver`: a worktree per issue, one PR per change, self-review
of the full diff, CI read twice before claiming done.

**Who merges.** The owner merges every PR of fleet-board itself (decision 2 of
ADR 0024). This is about building fleet-board, not about the goals the product
runs: those follow their consent (push, pr-open, merge by default, ADR 0008). No agent
merges, and no `arbiter-fleet` ruling stands in for the owner's merge. Nothing
mechanical protects `main` on this private repository (branch protection needs a
public repo or a paid plan), so PR 1 adds a client-side guard: `.githooks/pre-push`,
turned on by `git config core.hooksPath .githooks`, refuses a push to `main` and
runs the local gate. Until PR 1 lands, AGENTS.md is the only guard.

**The plan and its issues change together.** The body of each row's issue is
generated from its row by `scripts/plan-issues.py` (ADR 0026), so the issue
cannot drift from the plan. A PR that changes a row runs
`scripts/plan-issues.py --check` (it lists the issues that differ), and after
the owner approves, `--apply` writes them. The prep items of section 3 keep
their hand-written issue bodies. The PR description says the issue changed;
`fleet-issue-driver` checks it before declaring a PR ready. The milestone "v2 — dispatch" is closed and its issues
moved here ([ADR 0025](../adr/0025-v1-includes-dispatch.md)). The milestone
"v3 — remote" holds #64 (decision 22); its epic issue is not written yet.
