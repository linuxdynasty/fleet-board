# fleet-board design, part 2: deployment, configuration, open questions, milestones

Second half of [design.md](design.md), split out to keep each file under the 500-line cap.
design.md says what the product is: the goal, the architecture, goals and dispatch, reliability,
the views, scheduling, safety rails. This part says how it runs and ships: the three programs and
the `fleet` CLI, the configuration and `fleet.yaml`, what is still open, and the milestones. The same
order of authority applies: the protos, the ADRs (newest wins), the specs, the flows, then design.

## Deployment and CLI

**Three programs:** `fleetd` on the hub host, `fleet-agent` on each worker as the logged-in user,
`fleet` on the hub machine (v1: `local_trusted` binds loopback; from another machine use `ssh -L 7080:127.0.0.1:7080 <hub>`, same port on both sides; sign-in and remote access arrive in v3). **Joining:** the owner creates an enrollment token (`fleetd enroll-token` or the
Machines view); it embeds the SHA-256 of the fleet CA, so `fleet-agent enroll` verifies the hub
before sending anything (no trust on first use) and enrolling is retry-safe. The agent dials out; a
worker exposes nothing. Revoking a machine keeps its history and cuts its stream; delete only
follows a revoke. The hub's identity (CA key, account salt) is in `state.db`, so a **backup is a
secret**: `fleetd backup` and `restore` run on the hub host, never as API calls
([02g](specs/02g-agent-security.md)); the nightly backup keeps the last 7, made at 03:00 hub time.
**Install and upgrade:** GitHub Releases with checksums and an install script; `fleet-agent upgrade` and
`fleetd upgrade` fetch and verify the next release; no Apple signing for now. The hub laptop runs `fleetd`
under `caffeinate -s`; a Linux worker needs `loginctl enable-linger` once ([02j](specs/02j-hub-runtime.md#staying-awake-and-surviving-logout)).

**Listeners** ([02j](specs/02j-hub-runtime.md)): an mTLS agent port (default 7443) and a separate
API listener for the dashboard, CLI and attach. **Two modes** (from Paperclip): `local_trusted`,
plain HTTP on loopback with no sign-in; `authenticated`, HTTPS only, where people sign in with a
passkey or GitHub, scripts use API tokens and a phone is a paired device, after a one-time claim URL
the hub prints. In both, every request passes a Host and Origin check and a browser terminal attach
needs a single-use ticket ([02f](specs/02f-hub-authentication.md)). `local_trusted` governs the API
listener only; agents always use mTLS.

**`fleet` CLI:** `--json` is the stable interface, exit codes 0/1, `FLEET_URL` and `FLEET_TOKEN` for
scripts, `--yes` for destructive commands.

## Configuration

The hub's own file (`fleetd.yaml`: mode, listeners, certificates, GitHub App keys) is edited by hand
([02j](specs/02j-hub-runtime.md)). Everything else lives in the hub database; `fleet export` renders
it as `fleet.yaml`, the YAML form of `FleetConfig`
([config.proto](../api/fleet/hub/v1/config.proto)), with no secrets and no ids, and
`fleet import --dry-run` shows the plan before one all-or-nothing transaction. Enum values are shown
short:

```yaml
format_version: 1
hub: {auto_dispatch: false, prefer_non_hub: true, max_parallel_goals_per_repo: 3,   # defaults
      default_consent: [push, pr-open, merge]}
fleet_values: {max_active: 2, max_total: 4}      # the fleet default layer
machines:                     # matched by name; no addresses
  - {name: allens-Apple-MacBook-Pro, labels: [], values: {max_active: 4, max_total: 8}}
repos:                        # a repo is watched only when listed
  - {repo: {host: github.com, path: eshu-hq/eshu}, enabled: true,
     consent: [push, pr-open, merge], max_parallel_goals: 3}
accounts:
  - harness: claude
    spec: {display_name: claude-max, mode: fleet,   # or interactive-only
           reserve_percent: 25,   # kept for you: dispatch stops at 75 % used
           consent: [push, pr-open]}   # narrows the repo's consent
```

Effective consent is the repo's (else the hub default) intersected with the account's. RAM and
sign-ins are observed, and the hub address is not in this file. Subscription accounts are
discovered, so one no machine has signed in is skipped and blocks the import unless `allow_skipped`
is set; only API-key accounts (spent so far against an optional dollar limit, never auto-dispatched, such as OpenCode providers) are added by hand. A repo is
watched only when the owner adds it and the GitHub App is installed on it (`eshu-hq/eshu` is the
author's first). A machine named in an imported config that has not enrolled yet is recorded as drained when it enrolls (it takes no goals
until the owner undrains it); import never drains an enrolled machine.

## Open questions

Decided items moved into the sections above and the specs. Still open:

- **Terms of service** ([research](research/terms-of-service.md)). Anthropic is the highest risk
  (its Consumer Terms bar "automated or non-human means"); OpenAI documents multi-machine sign-in;
  Meta's terms are not read. Rule: never collect, store or proxy credentials; drive only unmodified
  harness binaries. Account modes ([ADR 0008](adr/0008-goal-consent-and-account-modes.md)) mitigate
  it. Get written confirmation from each provider before offering fleet-board to others.
- **Merge churn.** A per-repo cap on live goals exists (default 3, ADR 0019 #23); whether 3 is
  right, and what rebases and CI queues cost, is unmeasured.
- **Generic goal-file location** every harness sandbox can write without dirtying the checkout, and
  Codex and Muse hook payloads re-probed per version
  ([01d](specs/01d-goal-hooks.md#open-questions)).
- **Agent-ready issues.** Today a label from an allow-listed person (03j); an LLM triage pass that
  proposes it is not designed.
- **Prompt answers for Codex and Muse** need detection verified per version
  ([02h](specs/02h-goal-binding-and-pane-signals.md)); until it is verified, that prompt is shown
  as a notice with the `ssh -t ... tmux attach` line and cannot be answered from the dashboard.
- **Usage push sources:** [#46](https://github.com/linuxdynasty/fleet-board/issues/46). **Throwaway
  containers as workers:** deferred. **Tailscale:** optional, v3 only. **Measurements owed to the dispatch rows of v1:**
  [plan section 9](plan/v1-implementation-plan.md).

## Milestones

The [v1 plan](plan/v1-implementation-plan.md) turns the v1 milestone into PRs; the capability map in
[01](specs/01-agent-capabilities.md) tags each capability v1 or v3. There is no v2
([ADR 0025](adr/0025-v1-includes-dispatch.md)): what was once planned as v2 is part of v1.

- **v1, the fleet** (the GitHub milestone is "v1 - fleet"). It is built in two parts, watching first
   and then dispatch, and `v0.1` is tagged after both pass.
   - **Watching.** Agents enroll and connect over mTLS; tmux control mode, goal-file state,
     weekly usage probes with burn-down and waste projection (shown after the first 24 hours of a window); issues and PRs read by the hub through
     the GitHub App (the App stays read-only: a claim on an issue is posted by the harness through
     `gh`, never by the hub). Machines, Usage, Session, Triage, Work and Insights (efficiency) views,
     System health, and owner configuration of the
     hub itself: watched repos, reserves, the Codex account mapping, GitHub, machine admin (add, rename,
     labels, revoke, forget), Settings forms and `fleet repo|account|settings|machine|export|import`.
     Reads and writes through `fleet --json`, OTEL from the start.
   - **Dispatch.** `StartGoal` into panes and worktrees, goal hooks, claims, slot limits, drain,
     approvals and local approval, session control (send keys, harness commands, respawn, kill, new
     session, mark available), terminal attach (a ticketed WebSocket, also in `local_trusted`),
     session logs, routines, the written report and suggestions, Insights outcomes and stuck
     sections, the reliability rules, the trusted gate, and the `fleet` write commands (dispatch, goal
     control, requeue, routines). Every command that changes a pane passes the checks of
     [02h](specs/02h-goal-binding-and-pane-signals.md) (`SendKeys.purpose`) and the local policy.
     `HubSettings.auto_dispatch` is `false` by default, so dispatch runs only when the owner turns it
     on. Owner decisions 20 and 21 of [ADR 0024](adr/0024-pre-build-owner-decisions.md) (approvals in
     `local_trusted` mode, and the default goal text for repos without eshu's hooks) must be made
     before dispatch is switched on.

   Done when the seven-day run on the four machines passes (goals start, finish or go to Triage with a
   reason, no issue is taken twice, no goal runs into the reserve) and `v0.1` is tagged. `v0.1`
   freezes everything v1 implements; only the v3-only messages stay changeable until they ship
   ([02e](specs/02e-contract-rules.md)). The owner merges every v1 PR.
- **v3, remote.** `authenticated` mode (HTTPS, passkey and GitHub sign-in, API tokens, the claim
   URL), a paired phone with push, remote access, harness transcripts, and the optional Tailscale
   lookup for agents and phones on a tailnet (Tailscale is never required).

**Later, not scheduled** (named so nothing is lost; each needs its own decision before it is
built): GitLab, Jira, Bitbucket and Linear adapters ([ADR 0012](adr/0012-integration-model.md)) and
out-of-process adapter plugins; the OpenCode goal protocol; webhook ingress as a GitHub backstop;
the tmux popup switcher (`prefix + F`); an LLM pass that proposes `agent-ready`; throwaway
containers as workers; an optional hardening guide; the opt-in for harness transcripts (K3) in
v3. Every v1 command and RPC has its release in
[02k](specs/02k-v1-surface-and-cli.md).
