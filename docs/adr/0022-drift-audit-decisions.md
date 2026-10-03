# 0022. Drift audit decisions: pane context, hub runtime, dependencies, GitHub App

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Amends: [0001](0001-system-architecture.md) (package list),
  [0004](0004-agent-connectivity.md) (one TLS port),
  [0005](0005-github-app.md) (permissions),
  [0018](0018-hub-api-and-contract-amendments.md) and
  [0019](0019-pre-implementation-contract-decisions.md) (clearing a reused
  pane), [0020](0020-dependencies.md) (library table; see also
  [0023](0023-additional-libraries.md))
- Resolves: [0021](0021-insights-and-observability.md) (its open questions 2 to
  5 in decision 6; question 1 in the addendum)

## Context

An audit of the design documents against the protos found places where two
documents disagreed and the later ADRs and protos had to win, and a few
decisions no document had made. The fixes that are plain corrections went into
the specs. This ADR records the ones that decide something, so the earlier ADRs
they change point here.

## Decision

**1. Clearing the context of a reused pane is an owner choice.** An
auto-dispatch into a pane the fleet started (`TmuxSession.started_by_fleet`,
for example after a merged goal) may start with `/clear` (`fresh_context`), as
ADR 0018 and 0019 said. For a pane the developer marked "available for work",
clearing is an option the developer picks when marking, **off by default**:
`SetPaneAvailability.fresh_context_on_reuse` (agent and hub), carried on the
pane's `Availability` and reported in every snapshot. The scheduler sets
`fresh_context` only for a fleet-started pane or a mark with the option; in any
other developer-marked pane it sets nothing and the goal is typed into the
existing context. The agent refuses `/clear` otherwise
(`FRESH_CONTEXT_NOT_ALLOWED`). A developer's own session is still never driven
beyond that: the scheduler's nudge, `continue` and respawn stay limited to
fleet-started sessions ([02g](../specs/02g-agent-security.md)), so a stop in a
developer-marked pane is `STUCK` with a triage item.

**2. The hub has two listeners and a configuration file.** The agent mTLS
listener keeps ADR 0004's default port 7443; the API (dashboard, CLI, attach)
is a separate listener, loopback `127.0.0.1:7080` in `local_trusted` and an
explicit HTTPS address in `authenticated`. "One TLS port" in ADR 0004 means the
agent port. Configuration is one owner-edited file,
`~/.config/fleetd/fleetd.yaml`; state is `~/.local/state/fleetd/`; the start
command is `fleetd run`. Detail: [02j](../specs/02j-hub-runtime.md).

**3. Ids.** Lowercase ULIDs (02e) use `github.com/oklog/ulid/v2`
(Apache-2.0), lowercased, monotonic within a millisecond. The two other
libraries the v1 plan needed, mDNS and system sampling, are decided in
[0023](0023-additional-libraries.md); this ADR first proposed running `dns-sd` or
`avahi` and reading `/proc`, and 0023 replaces that with a library after
building the candidates. Versions are pinned and licenses checked in the PR that
first imports them (ADR 0020, rules for changing dependencies).

**4. GitHub App permissions.** Read-only, repository permissions only:
Metadata, Issues, Pull requests, Checks and Commit statuses (read), plus
**Contents: read**, which the `PathExists` call behind the
`REPORT_WRITTEN` done rule needs (03i); it may be left off while no routine
uses that rule. No webhook events (polling, ADR 0005), no write permission, no
account permission. Issue timeline events (who applied the trusted label) come
with Issues, and `reviewDecision` and `statusCheckRollup` with Pull requests,
Checks and Commit statuses. For GitHub sign-in only: enable the App's user
authorization, callback URL `<public origin>/auth/github/callback`, with
expiring user tokens on; skip it when sign-in is passkey only. `fleetd setup
github` writes a manifest with this set.

**5. Package layout.** The v1 plan's section 4 refines ADR 0001's package
list: `internal/store` and `internal/telemetry` are shared by the hub and the
agent, the hub splits into `gateway`, `ca`, `ingest`, `pacing`,
`integration/github`, `syncer`, `api`, `system` and `insights`, the agent into
`conn`, `ring`, `tmux`, `probe`, `goals`, `policy` and `doctor`, and harness
adapters stay at `internal/harness/`. `scheduler`, `verifier` and `triage`
packages arrive with v2.

**6. Owner questions in ADR 0021 are answered as proposed.** Report text needs
`READ`; real repo names go to the model while machines and accounts are
aliased; the cron default is Monday 07:00; suggestions in Triage default off;
`/metrics` takes an `ADMIN` token or a loopback peer; System thresholds are the
hub's defaults, not settings, in v1; `agent.log` rotates at 10 MiB, 3 files.
**Question 1, whether the built-in weekly report runs from first start, was
open here; the owner answered it (see the addendum below):** on by default,
spare quota only.

**7. Size estimates are dropped.** The S/M/L estimate, the learned cost per
size and the pause at two to three times the estimate (design.md scheduling)
are not part of the product. Insights may still report size against actual
cost where [05](../specs/05-insights.md) says so.

**8. Smaller rulings.** `RunRoutine` of a spare-quota routine with no spare
account is a `WARN` the owner can accept (03i). `review_rounds` counts
changes-requested reviews and never resets (03b). `STALE` begins at three probe
intervals everywhere; 05b and 03d name later escalations only. Heartbeat
`last_seen` is written at most once a minute. Every default is in
[defaults](../specs/defaults.md).

## Consequences

- The scheduler and agent code for reused panes needs `started_by_fleet` and the
  mark's option; the dashboard's "Mark available" gets one checkbox.
- ADR 0020's table gains the ULID line here and two more in 0023; a new
  library still needs a line or an ADR.
- An owner who wants merged panes cleaned in developer sessions ticks the option
  per pane; nothing is cleared otherwise.
- The first v1 PR set needs a way to configure repos, reserves and GitHub
  (02j, "Configuring a fresh hub"); the v1 plan must schedule that PR.

## Addendum, 2026-10-02: owner decisions with no written home, and the second drift audit

A second audit compared the plan, the specs, the protos, the mockup and the
GitHub issues with the owner's decisions of 2026-09-28 to 2026-10-02. This
addendum records what that audit changed. The accepted text above is not
rewritten; where this addendum differs, it wins (the newest ADR text wins).

**A. Owner decisions that had no ADR, and where their detail lives.**

1. **Pass-through.** No root, no sandbox, harness credentials are never read,
   stored or proxied, and no harness runs headless (`-p`). The product drives
   unmodified interactive harnesses in tmux. (AGENTS.md principle 1; 01
   principles; `fleet-security`.)
2. **Prove first.** A risky theory needs a spike or probe before code is built on
   it. (AGENTS.md, Evidence Rules; the plan's section 3, items 0.5 to 0.9.)
3. **The weekly insights report is on by default.** It runs only on spare
   subscription quota, never into the reserve and never on an API-key account;
   it spends subscription quota, never API money. This answers question 1 of
   [0021](0021-insights-and-observability.md) and the "still open" note above.
   (05 section 11; defaults.md.)
4. **The reserve line** stops dispatch and raises an alert; auto-pausing running
   goals is an opt-in setting, off by default; `max_parallel_goals_per_repo`
   defaults to 3. (03b, 03c; defaults.md.)
5. **Goal hooks ship as a plugin package for Muse**, and one Codex-format hook
   definition serves Codex, Claude and Muse. (01d; the repo's `hooks/` directory
   arrives with v2.)
6. **Architecture: stream where a push source exists, poll only usage and
   GitHub.** Push sources: tmux control mode, goal-file watching, harness hooks,
   the agent's gRPC stream, `Watch`. Polled: usage (10 minutes, plus event
   triggers and passive readings; issue #46 probes for a push source) and GitHub
   (conditional requests; webhooks optional later). (design.md, "Inputs on each
   machine".) Two polls in the specs are not covered by this rule: typing
   detection by `tmux list-clients` every 2 s, and the goal-file "slow rescan".
   They stand until the tmux probe (plan 0.5) reports; see question 4.
7. **v1 is read-only,** the plan is approved and merged and its status is
   Accepted; issues #25 to #48 are in the milestone "v1 - read-only fleet" (#46
   the usage push probe, #48 PR 13a). v2 is dispatch, v3 is remote.
8. **No AI attribution** in commits, PRs or docs. Older history keeps its
   trailers. New commits are checked by `scripts/check-attribution.py` (run by
   `verify-agent-canon.sh`; CI runs it from plan PR 1), and Claude Code's own
   attribution lines are off in `.claude/settings.json`.
9. **Order of authority when documents disagree:** the protos, then the ADRs
   (the newest wins), then the specs, then the flows, then design.md.
   (AGENTS.md, Documentation Discipline; the ADR and spec indexes; design.md.)

**B. Corrections, not new decisions.**

1. **v1 includes three hub parts that decision 5 of this ADR (package layout) placed in v2.** The v1
   plan needs a hub that sends commands to agents (probes are commands:
   `EnsureProbe`, `StopProbe`, and `CapturePane`), assigns one probe per account
   with failover, and raises triage items for the kinds a v1 source can raise.
   So v1 builds the **command path** (`internal/hub/outbox`), **probe
   assignment** (`internal/hub/pacing/probe`) and the **raise and retire of
   triage items** in `ingest`, and an `internal/hub/config` shell. The
   dispatching scheduler, the verifier and the dispatch-era triage kinds stay
   in v2. (Plan rows 2b, 5, 10.)
2. **`default_server` defaults to `control`** (ADR 0011). design.md said `none`
   and the `fleet-tmux` skill said `none` and `-L fleet`; both are corrected.
   defaults.md and 03e carry the value and the local policy file keys.
3. **`filippo.io/age`** joins ADR 0020's table: 02g already says
   `fleetd backup --encrypt` wraps the backup with `age`. Version and license are
   pinned and checked in the PR that first imports it (plan PR 3).
4. **`base_cap` is a fixed 2 per account;** nothing is learned (defaults.md).
5. **"Owner notified" means a Triage item and a browser indicator in v1 and
   v2.** The phone push is v3 (question 8).
6. **A new account starts with consent push, pr-open, merge** (ADR 0008), so a
   discovered account never blocks dispatch with an empty consent.
7. **Probe pane on a startup screen** (trust, update, release notes, login) is a
   probe failure `STARTUP_PROMPT_SEEN` with a Triage item; the agent types
   nothing (03b).
8. **The hub keeps no 24-hour resource chart:** the `resource_samples` table is
   gone from 03f and 03g; only the latest sample is held.

**C. Questions for the owner.** Nothing below is decided; each names the document
that waits on the answer.

1. **Machine administration in v1.** Add machine (enrollment token), rename and
   labels, Revoke and Forget are listed in design.md's Machines view, but no v1
   plan row built them. Recommended: v1 (a stolen laptop must be revocable before
   `v0.1`). 02k and plan PR 13a hold them as "owner to confirm".
2. **Local policy file defaults** ([03e](../specs/03e-settings-and-drift.md)):
   what a fresh agent allows for `allowed_repo_roots` (none until set, or a path
   such as `~/repos`) and `allowed_harnesses`, and whether `fleet-agent enroll`
   writes a starter file or asks for the roots.
3. **Minimum elapsed time before the waste projection is shown** (03b): a tiny
   elapsed fraction makes `projected_unused_percent` noisy.
4. **Typing detection.** If the tmux probe finds no push source, may the 2 s
   `list-clients` poll stay as an accepted exception to the architecture rule
   (A.6: poll only usage and GitHub)? The same for the goal-file rescan.
5. **Backups:** how many nightly backups to keep and at what hour (proposed: the
   last 7, at 03:00 hub time).
6. **Thresholds with a name but no number** (proposed, not approved): pause
   log shipping below 5 GiB free and resume above 8 GiB; agent `DISK_LOW`
   below 5 GiB under any repo root; goal-file rescan every 60 s; allowed ranges
   for `reserve_percent` (the mockup uses 0 to 90), `max_active` and `max_total`.
7. **The default goal text for repos that do not ship eshu's hooks** (the claim
   step, the `BLOCKED` convention, the review-loop cap): the wording carries the
   policy a non-eshu goal runs under, so the owner approves it before it goes
   into 03c.
8. **v3 phone:** an installable web app with web push, or a native app with
   APNs or FCM, and which push channel comes first.
9. **Remote `fleet` or dashboard before v3:** is an `ssh` forward enough, or is
   an earlier authenticated mode wanted? (`local_trusted` binds loopback.)
10. **Dev-tooling headless exception.** AGENTS.md lets `agent-roles.py
    codex-exec|muse-exec` run a role headless for development of this repo; the
    skills and agent-guide say "never headless" with no exception. Is the
    exception intended? If so, the skills get the same sentence.
11. **GitHub fine-grained token fallback** (ADR 0005, 03b): build it in v1, or
    only the App?
