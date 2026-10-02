# Contract review, 2026-10-02 (before implementation)

Second full pass over the contracts before development starts. Six Sonnet
reviewers each took one angle (agent wire vs capabilities, agent wire vs
workflows, hub API vs dashboard and CLI, hub API semantics and auth, goal
lifecycle end to end, proto hygiene and doc consistency). A separate Sonnet
verifier per angle tried to refute every finding against the files, and a
completeness critic looked for cross-cutting gaps. 73 findings survived
verification and the critic added 13: **86 in total**. None was rated a
blocker on its own; about 20 carry a high refactor risk if deferred.

Full detail (evidence with file:line, problem, proposed fix, verifier note)
is in [2026-10-02-contract-review-findings.json](2026-10-02-contract-review-findings.json).
Owner decisions taken from this review are in
[ADR 0019](../adr/0019-pre-implementation-contract-decisions.md).

## Themes

| Theme | Findings |
|---|---|
| A. Hub API conventions | 16 |
| B. Auth, identity, security | 11 |
| C. Goal binding, reuse, pane signals | 22 |
| D. Delivery, sequencing, logs, settings wire | 11 |
| E. Hub domain resources and goal lifecycle | 21 |
| F. Integration contract | 3 |
| G. Doc drift | 2 |

## Findings

Severity: I important, M minor. Risk: refactor risk if deferred (h/m/l).

| Theme | Id | Sev | Risk | Finding | Status |
|---|---|---|---|---|---|
| A | `watch-filter-resume-contradiction` | I | h | Watch: changing the filter is both 'resume with last token' and RESYNC_REQUIRED; pane-visible filtering has no grammar | fixed |
| A | `version-etag-vs-volatile-fields` | M | h | `version` bumps on every committed change, so expected_version is unusable on resources that carry live status | fixed |
| A | `scope-model-incomplete` | M | h | Scope model has no table for Watch topics, 'self' rules live only in proto comments, and READ is too broad for a phone | fixed |
| A | `watch-subscription-model` | I | h | Watch has one filter for all topics, and changing the filter contradicts RESYNC rules; pane-summary subscription cannot work as designed | fixed |
| A | `etag-volatile-status` | I | h | `version` as ETag covers volatile status on Machine, Account and Goal, so expected_version fails constantly | fixed |
| A | `volatile-fields-in-watched-resources` | I | h | Telemetry and log counters inside versioned Watch resources (Machine, Session) contradict the version/dedupe rule and the load model | fixed |
| A | `consent-acts-free-strings` | I | h | Consent acts are `repeated string` in six messages although they are a closed set typed into the harness | fixed |
| A | `naming-units-and-json-conventions` | M | h | Inconsistent timestamp suffixes, units, id field names and string-typed enums across packages | fixed |
| A | `contract-freeze-gate` | I | h | Additive-only rule is already in force before any implementation, and the confirmed fixes need non-additive changes | fixed |
| A | `logs-scope-mismatch` | I | m | Logs are READ scope in the API but owner-only in ADR 0010; a viewer or phone device can read all terminal history | fixed |
| A | `filter-grammar-and-field-catalogue` | I | m | List/Watch filter fields named in request comments are not fields of the resource, and the grammar is undefined | fixed |
| A | `workitemref-identity-undefined` | M | m | WorkItemRef mixes identity with display fields and is used as the Watch/dedupe id | fixed |
| A | `codegen-config-and-64bit-ints` | M | m | No buf.gen.yaml or TypeScript/Go codegen decisions; uint64 versions become bigint/strings in JSON and TS | fixed |
| A | `error-model-gaps` | M | l | Error model leaves common failures without reason codes; field-level validation and WebSocket errors unspecified | fixed |
| A | `capability-map-and-size-limits-incomplete` | M | l | Command-to-capability map and hub API size limits are not specified; payload caps equal the message cap | fixed |
| A | `small-doc-proto-contradictions` | M | l | Minor scope and idempotency contradictions between 02b and the protos | fixed |
| B | `no-signin-flow` | M | h | No sign-in, claim, session or API-token flow for authenticated mode; credential carriage is undefined | fixed |
| B | `local-trusted-attack-surface` | I | h | local_trusted mode and WebSocket attach: no Origin/Host rules, no attach ticket, principal and auth_mode type undefined | fixed |
| B | `hub-identity-and-restore` | I | h | Hub identity material (CA key, fleet_id, account salt, settings_epoch) is absent from the data model and backup, and nothing mints a new settings_epoch on restore | fixed |
| B | `device-role-vs-scope` | I | m | Roles and scopes are mixed: what Role does a narrowed device or token send to the agent, and which check wins? | fixed |
| B | `scheduler-role-permissions` | I | m | No Role x CommandClass x kind matrix, so what the hub's own SCHEDULER principal may do on the agent is undefined | fixed |
| B | `attach-websocket-contract-gaps` | I | m | Browser terminal attach has no error channel, auth or framing spec, and ADR 0016 names the wrong messages | fixed |
| B | `enroll-trust-anchor-and-retry` | I | m | Enrollment trusts whatever CA the server presents (TOFU), and a lost Enroll response strands the agent | fixed |
| B | `machine-revoke-lifecycle` | I | m | Machine deletion and revocation have no state, cascade or live-connection behaviour | fixed |
| B | `connect-enroll-failure-wire` | M | l | Enroll/Connect-level failures (token reasons, fleet mismatch, unknown machine) have no typed representation | fixed |
| B | `local-actions-not-audited` | M | l | Security-relevant agent-side decisions have no permanent audit mapping | fixed |
| B | `no-trace-context-on-wire` | M | l | Docs promise hub-to-agent traces, but Command, Event and Settings carry no trace context | fixed |
| C | `goal-binding-reused-pane` | I | h | Goal identity for hooks is bound by pane env or by cwd, and neither works for a reused session | fixed |
| C | `reuse-workdir-mismatch` | I | h | Session reuse has no check that the pane is in the goal's repo or worktree | fixed |
| C | `keystroke-signal-goal-panes` | I | h | The developer-keystroke signal that suppresses nudges and drives the typing banner has no wire carrier for goal panes | fixed |
| C | `reuse-path-goal-identity` | I | h | Reused-session goals cannot get FLEET_GOAL_ID or the right cwd, so hooks, goal registry and adoption all break on the reuse path | fixed |
| C | `goal-pause-interrupt-state` | I | h | Developer interrupts and harness-side pauses have no wire representation and contradict W6's nudge rule | fixed |
| C | `goal-pane-input-and-ready-signals` | I | h | No wire signal for keystrokes or ready-prompt state on goal panes, but W6 depends on both | fixed |
| C | `owner-interrupt-and-pause-reason` | I | h | A person interrupting or pausing a goal at the machine (owner-paused) has no wire event, condition kind, phase or triage kind, and 03c contradicts 01d; PAUSED has no reason | fixed |
| C | `pane-attribute-events-and-internal-sessions` | I | h | No event for mutable pane/window/session attributes, and internal tmux sessions (attach view, probe server) have no hub treatment | fixed |
| C | `interrupt-pause-no-wire-state` | I | m | Interrupt and pause have no wire representation, although 01d says they are reported as a HarnessCondition | fixed |
| C | `goal-subcommand-support-not-advertised` | M | m | The hub is told to check /goal subcommand support before sending, but the agent never reports it | fixed |
| C | `sendkeys-overloaded-gating` | M | m | SendKeys is used for nudges, Claude resume and limit-wait resume, but its gating is tied to prompt answering and it lacks an idle check | fixed |
| C | `availability-mark-session-vs-pane` | I | m | SetSessionAvailability addresses a tmux session but availability is a per-pane fact | fixed |
| C | `startgoal-steps-stale-vs-goalspec-and-01d` | M | m | The StartGoal composite step list and its retry/idempotency classification omit steps that GoalSpec and 01d require | fixed |
| C | `harness-goal-control-capabilities` | M | m | Per-harness goal-control actions (pause, resume, continue) live in hub code, not on the wire; LIMIT_WAIT resume cites a probe that did not cover it | fixed |
| C | `startgoal-steps-and-reasons` | I | m | StartGoal composite omits steps and reason codes the specs require (register, goal file, model/effort/clear, startup prompts) | fixed |
| C | `startgoal-composite-omits-model-effort-clear` | M | m | GoalSpec.model/effort/fresh_context have no composite steps, failure reasons, role path or auto-dispatch defaults; per-model pacing has no input when model is empty | fixed |
| C | `reuse-vs-worktree-and-prune-order` | I | m | Session reuse contradicts agent-ensured worktrees (harness cwd cannot change), the hub cannot see a pane's workdir, and prune is ordered before KillWindow | fixed |
| C | `repo-driver-branch-visibility` | I | m | For REPO_DRIVER goals (eshu, the primary repo) the hub never learns the branch or worktree, breaking CR lookup by head_ref and the one-agent-per-branch rule | fixed |
| C | `repo-identity-derivation` | M | m | How the agent derives RepoRef from a checkout (remote choice, forks, multiple checkouts, code_host) is unspecified | fixed |
| C | `hook-trust-and-stale-names` | M | l | Hook trust is a Codex-only bool with no unknown state, and several contract names have no capability behind them | fixed |
| C | `slots-full-ownership` | M | l | SLOTS_FULL and max_active enforcement have no owner: agent 'does not compute slots' but a preflight code and a local ceiling assume it does | fixed |
| C | `id-format-and-new-session-name` | M | l | Opaque ids are used as tmux names, env values, file paths and resource keys with no format rule; StartGoal's new-session name is ambiguous | fixed |
| D | `logstreamkey-session-id-for-developer-sessions` | I | h | LogStreamKey requires a hub-assigned SessionId the agent cannot know for developer sessions | fixed |
| D | `forge-status-snapshot-only-unfit-never-clears` | I | m | Durable preflight reasons clear 'on the next snapshot or event', but forge CLI status and folder trust have neither | fixed |
| D | `seq-epoch-monotonic-counters` | I | m | Agent event seq and hub connection epoch can regress after a crash or restore, silently discarding events or commands | fixed |
| D | `event-gap-and-replay-apply-rules` | M | m | Seq-gap detection cannot tell shedding from loss, and 'replayed seq <= Snapshot.seq is history only' contradicts the W2 failure rows | fixed |
| D | `session-log-ack-gap-semantics` | I | m | SessionLog hole/duplicate handling has no negative ack, gap_before offset semantics are undefined, and a restored hub wedges streams | fixed |
| D | `dev-session-log-key` | M | m | SetLogCapture cannot give the agent a SessionId, so LogStreamKey.session_id is empty for developer sessions | fixed |
| D | `settings-skew-and-probe-interval-merge` | I | m | An older agent silently ignores new tighten-only settings, and probe-interval merge and precedence are undefined | fixed |
| D | `delivery-class-table-incomplete` | M | l | The event delivery table covers 16 of 28 body kinds, and sequence gaps are ambiguous | fixed |
| D | `local-confirm-interim-vs-single-result` | M | l | AWAITING_LOCAL_CONFIRM is an 'interim CommandResult', contradicting 'exactly one CommandResult' and ResultAck | fixed |
| D | `settings-policy-failure-wire` | M | l | Settings rejection and policy safe mode have no wire fields although W10 says the UI shows them | fixed |
| D | `agent-clock-time-authority` | M | l | Agent-clock timestamps drive ordering and waits with only a 5-minute warning for skew | fixed |
| E | `per-repo-config` | I | h | Per-repo settings (consent, parallel cap, tracker-to-repo mapping, hooks mode) have no home: Integration.watched is a list of strings | fixed |
| E | `goal-active-set-undefined` | I | h | GoalPhase has no queued/expired/abandoned phase and 'active' is never defined, so the uniqueness constraints and slot accounting cannot be implemented | fixed |
| E | `external-provider-transitions` | I | h | Provider-side changes while a goal runs (issue closed or cancelled, change request merged by a human or closed unmerged) have no transition; CANCELLED has no phase; goal end states have no work-item stage mapping | fixed |
| E | `repo-level-config-missing` | I | h | Per-repo settings referenced by dispatch (consent, parallel cap, worktree mode, branch) have no home in the contract | fixed |
| E | `goal-history-api` | I | m | Goal run history and timeline are stored by the hub but exposed nowhere: no start/end time, restarts, usage consumed, transcript or transitions | fixed |
| E | `logs-api-gap` | I | m | Session-log management API missing: no list of stored logs, delete, search, store usage or retention setting, and no read path for archived pane summaries | fixed |
| E | `dispatch-contract` | I | m | Dispatch panel: PreflightGoal request is incomplete and the 'manual allowed with a warning' path (DISPATCH_CONFIRM) is unwired | fixed |
| E | `account-lifecycle` | M | m | Accounts can only be updated, never created or removed; Codex mapping and API-key (OpenCode) accounts have no creation path and Account has no provider field | fixed |
| E | `config-export-import` | M | m | `fleet export` / `fleet import --dry-run` and 'imported machines start drained' have no API; machine records cannot be pre-provisioned | fixed |
| E | `admin-cli-rpcs` | M | m | CLI commands that must go through the hub (backup, API tokens and login bootstrap, store status, debug stream) have no RPC | fixed |
| E | `goal-hooks-trigger` | I | m | Nothing triggers or exposes EnsureGoalHooks: no reconcile step and no hub RPC, yet generic-hook repos depend on them | fixed |
| E | `rearm-after-done` | I | m | DISAGREE -> RUNNING ('not done') has no working mechanism: the harness goal is already achieved/cleared and the goal file still says DONE | fixed |
| E | `limit-wait-missing-from-state-machine` | I | m | LIMIT_WAIT exists in the proto and ADR 0018 but not in the 03c state machine; entry, exit, slot class and missing-reset cases are undefined | fixed |
| E | `routines-not-in-workflows` | I | m | Routines have a contract but no workflow: run creation, numbering, trigger evaluation, done-rule inputs and snapshotting are unspecified and some done rules are unverifiable | fixed |
| E | `goal-resource-lacks-what-w6-persists` | M | m | Goal cannot render several states and histories W6 persists or the UI requires | fixed |
| E | `start-failure-reaction-table-incomplete` | I | m | Start-failure handling table is incomplete and contradicts the agent contract on DISK_LOW and version support; drain interplay with outbox and respawn is unspecified | fixed |
| E | `ended-sessions-logs-and-summary-archive-unreachable` | I | m | No hub API to find ended sessions, read the archived last pane summary, or delete a session's logs | fixed |
| E | `hub-dispatch-refusal-reasons-missing` | M | m | Hub-side dispatch refusals have no ReasonCode, so PreflightGoalResponse.hub_checks cannot say why | fixed |
| E | `workflow-state-names-missing-from-enums` | M | m | Triage kinds and goal state named in 03c/03d/03e do not exist in the hub enums | fixed |
| E | `scheduler-explanation-state-unexposed` | I | m | Why the fleet is not using an account or machine is stored or logged but has no API field | fixed |
| E | `small-hub-api-gaps` | M | l | Smaller mismatches: audit filter lacks target/session, read-only toggle has no wire message, machine labels ambiguity, next-up ordering | fixed |
| F | `integration-service-operational-vocabulary` | I | h | IntegrationProviderService cannot carry rate limits, conditional requests or typed provider errors that the sync design depends on | fixed |
| F | `integration-cannot-express-verification` | I | m | IntegrationProviderService cannot express the queries W4/W6 rely on, and ChangeRequest lacks the fields W6 reads | fixed |
| F | `trusted-gate-not-computable` | I | m | The trusted-issues gate cannot be evaluated from the integration contract: label actor is not carried, and the allow-list is a flat string list | fixed |
| G | `workflow-text-drift` | M | l | Stale or inconsistent workflow text that will mislead implementers (small, mostly doc-only) | fixed |
| G | `stale-docs-contradict-adr-0015-and-0018` | I | l | Specs still describe superseded designs (log files, owner strings, SUPPORTED-only dispatch, goal.stop) | fixed |

## Outcome

Verified against the protos and specs on the `contracts-review-2` branch after
the seven theme commits (buf build and buf lint exit 0, every file under 500
lines except this review's machine-written JSON data file, every backticked
message, enum, RPC and field name in the specs and ADRs 0014 to 0019 resolves
to a proto or is marked as a historical name).

| Status | Count |
|---|---|
| fixed | 86 |
| deferred | 0 |
| wont-fix | 0 |

No finding is deferred as a whole. A few fixed findings carry a part that was
declined or is tracked for later, each with its reason and where it is tracked:

| Finding | Part not done | Reason and tracking |
|---|---|---|
| `scope-model-incomplete` | a separate approvals-read scope | `READ\|APPROVALS` rows cover the phone; can be added later without breaking anyone (02d) |
| `error-model-gaps` | rate-limit values in `GetServerInfo` | values stay in hub configuration until code needs them (02b, error table and limits) |
| `interrupt-pause-no-wire-state` | `GOAL_LINE_STATE_PAUSED` | the goal file still says WORKING, so the pause is a `HarnessCondition` (02h) |
| `account-lifecycle` | `MergeAccounts` | wont-fix: a stale duplicate is deleted once signed out (02b, account.proto) |
| `small-hub-api-gaps` | `total_size`, `queue_rank` | wont-fix: speculative, no screen needs them (02b) |
| `rearm-after-done` | probe of the Codex and Muse re-arm form | needs a live harness; recorded as unverified in 01b |
| `harness-goal-control-capabilities` | limit-resume probe | `GoalControl.limit_resume_verified` is false until probed (01b, 02h) |
| `codegen-config-and-64bit-ints` | `go.mod` and CI wiring | belongs to implementation step 0; item 4 of the freeze checklist in 02e |
