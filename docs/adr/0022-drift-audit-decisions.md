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
**Still open:** whether the built-in weekly report runs from first start. It is
built as on (the routine is created enabled; the spare-quota guard keeps it to
quota that would go unused); the owner can answer off before v2 ships it.

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
