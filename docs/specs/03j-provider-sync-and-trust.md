# 03j. Provider interface in use: queries, polling state, errors, trust

- Part of: [03-workflows.md](03-workflows.md); extends W4 in
  [03m](03m-work-source-and-code-host-sync.md) and the verifier in W6 ([03l](03l-goal-lifecycle-and-verification.md))
- Contract: [`api/fleet/integration/v1/`](../../api/fleet/integration/v1/)
  (`integration.proto` capabilities, `model.proto`, `sync.proto`,
  `service.proto`) and [`trusted_gate.proto`](../../api/fleet/hub/v1/trusted_gate.proto)
- Decision: [ADR 0019](../adr/0019-pre-implementation-contract-decisions.md), 31 and 32

The hub's syncer and verifier were specified in GitHub terms (ETag, `304`,
`x-ratelimit-*`, `headRefName`, `LABELED_EVENT`). This file says how each of
those is carried by the provider-neutral service, so a GitLab, Jira or plugin
adapter can serve the same loops, and who owns which state.

## Who owns what

The **hub owns the polling loop and all of its state**: cursors, validators,
rate-limit state, back-off, health. `fleet.integration.v1` adapters keep no
sync state: each request carries the cursor and validators, each response
carries the next ones, the rate limits and a typed error. An adapter can be
restarted, replaced by an out-of-process plugin, or run twice without anyone
losing a cursor, and the hub can compute `Integration.status.health`,
`sync_lag`, back-off and `integration_access_lost` without parsing text. The
adapter holds the credential (ADR 0005) and refreshes it itself; it never
sleeps on a rate limit, it reports it.

## Queries

| Need (workflow) | Call | Fields |
|---|---|---|
| Changed work items of a project, every 60 s (W4) | `ListWorkItems` | `project`, `since` (the last round's `next_since`), `validators` (this page's), `include_closed` (true once a cursor exists, so a close is seen), `evidence_labels` (the trusted labels, below) |
| Claim re-check before dispatch (W5), body for a manual dispatch | `GetWorkItem` | `ref`, `include_body`, `evidence_labels`; the response's `item` is what the gate is re-run on |
| Changed change requests of a repo (W4) | `ListChangeRequests` | `repo`, `since`, `validators` |
| Verify goals by head ref, up to `max_batch_size` per call (W4, W6) | `ListChangeRequests` | `repo`, `source_branches[]` (the branches agents report) and/or `head_commits[]` (a goal's `cr_head_sha`), `states` empty = all, because the change request is merged by the time it is verified |
| Fallback when no head ref matches (W6) | `ListChangeRequests` | `repo`, `linked_work_item` (change requests that close or reference the goal's item) |
| Verify now: goal said `DONE` (W6) | `GetChangeRequest` | `ref`; at most one per 10 s per goal |
| Report file exists on the pushed branch (03i) | `PathExists` | `repo`, `ref`, `path` |

What W6 reads off a `ChangeRequest`:

| W6 reads | Field | Computed by |
|---|---|---|
| Merged, closed, cancelled | `state`, `merged_time`, `closed_time`, `merged_by` (a merge by a person: ADR 0019, 25) | adapter |
| Which goal's branch | `source_branch`, `head_commit`, `head_repo` (set for a fork; compare it before trusting a name match) | adapter |
| "Approved and green" (no MERGE consent) | `review_decision = APPROVED` **and** `checks.rollup = SUCCESS`, both about `head_commit` | adapter, from branch protection, required reviewers, CODEOWNERS and required checks; the hub never derives either from `reviews[]` or the counts |
| Review-loop cap | `review_rounds` | adapter |
| Linked items | `linked_work_items[]` | adapter |
| Would it merge now | `merge_state` | adapter |

An adapter that cannot report a value leaves it `UNSPECIFIED` and the verifier
reads that as "not known", never as success: a code host without
`capabilities.reviews` or `checks` can still be verified by merge, not by
"approved and green".

GitHub mapping for the first adapter: `review_decision` from
`reviewDecision` (`REVIEW_REQUIRED` is `REQUIRED`), `rollup` from
`statusCheckRollup.state` over the required contexts, `merge_state` from
`mergeStateStatus`, `merged_by` from `mergedBy`, `head_repo` from
`headRepository` when it differs from `baseRepository`.

## Cursors and validators

- `since` is what the last **complete** round returned in `next_since`; the
  hub never builds one from its own clock. A time-only provider returns a
  timestamp, widened by a small overlap inside the adapter; the hub's
  `update_time` guard (03m, "Ordering") drops the repeats. A provider with
  a real cursor returns an opaque string.
- `validators` belong to one query and one page: the hub stores them in
  `sync_cursors{integration, resource, page, etag, last_modified_time, since}`
  and sends them back only with the same query and `page_token`. A response
  with `not_modified = true` has no items and changes nothing; for a provider
  that sorts by update time, `not_modified` on page 1 ends the round.
- `CURSOR_EXPIRED` (provider dropped the cursor, a page token went stale)
  makes the hub forget `since` and run a full poll without validators. The
  periodic full poll (every 30 min) does the same on purpose.
- Providers with no validators (Jira, Linear) never return `not_modified`;
  they filter by `since` only.

## Rate limits

Every response, including `not_modified` and errors, carries `meta`:
`rate_limits[]` (kind `CORE`, `GRAPHQL`, `SEARCH`, `SECONDARY`; limit,
remaining, `reset_time`), `cost` (GraphQL points) and `provider_time` (the
provider's clock, to see skew). The hub stores the latest per kind in
`rate_limit_state{integration, kind, limit, remaining, reset_time}`, mirrors it
to `Integration.status.rate_limits`, and decides:

| State | Hub does |
|---|---|
| `remaining` under 10 % of `limit` | poll intervals times 4 |
| `remaining = 0` | no call of that kind before `reset_time`; `health = BACKING_OFF`, `health_reason = RATE_LIMITED`, `backoff_until_time = reset_time` |
| error `RATE_LIMITED` | same, using the error's `retry_after` or `rate_limit.reset_time`; for `SECONDARY` halve request concurrency for 1 h |
| 304 cost | learned from `rate_limits` before and after; ADR 0005's open question (are 304s free?) is answered by data, not by a constant |

## Errors

Every failed call is an RPC error carrying a `ProviderError` detail.
`retryable` says whether the same request may succeed later; `retry_after` is
the earliest sensible retry.

| `ProviderErrorKind` | RPC code | `retryable` | Hub does |
|---|---|---|---|
| `NOT_FOUND` on a watched project or repo | `NOT_FOUND` | no | `health = DEGRADED`, `ACCESS_LOST`; triage `integration_access_lost{integration, project}`; dispatch for its repos stops |
| `NOT_FOUND` on one item | `NOT_FOUND` | no | the item is gone: mirror row closed, a goal under it handled by the provider-side rules in 03h |
| `UNAUTHENTICATED` | `UNAUTHENTICATED` | no | `DEGRADED`, `CREDENTIALS_INVALID`; the owner must act |
| `FORBIDDEN` (not a rate limit) | `PERMISSION_DENIED` | no | as `ACCESS_LOST` |
| `RATE_LIMITED` | `RESOURCE_EXHAUSTED` | yes, after `retry_after` | `BACKING_OFF`, `RATE_LIMITED` |
| `UNAVAILABLE` | `UNAVAILABLE` | yes | `BACKING_OFF`, `PROVIDER_UNAVAILABLE`; exponential back-off, at most 5 min |
| `CURSOR_EXPIRED` | `ABORTED` | no, as sent | drop the cursor, full poll (above) |
| `UNSUPPORTED` | `INVALID_ARGUMENT` | no | a hub bug: log it, never retry; the hub checks `Describe` first |

`health_reason` and the last `ProviderError` are on `Integration.status`
(`health_reason`, `health_message`, `last_error`); `fleet_hub_integration_state`
carries the reason as a label. A successful round clears them. Health returns
to `OK` only after one complete round without error.

## The trusted-issues gate

The adapter is read-only and does not know the allow-list. It returns
**evidence**; the hub decides (rule and messages in
[`trusted_gate.proto`](../../api/fleet/hub/v1/trusted_gate.proto)).

**Evidence** (`fleet.integration.v1`): `WorkItem.author` and `create_time`;
`WorkItem.labels[]` where each `Label` for a name in `evidence_labels` has
`applied_by` and `applied_time` of the latest apply (GitHub `LABELED_EVENT`,
GitLab label events, Jira changelog; a status-driven tracker reports the
transition into the status as the label); `WorkItem.last_editor` and
`last_edit_time` (title or body edits, not comments). `Person.provider_id`
is the stable account id. The adapter declares `labels_with_actor` and
`edit_evidence`; the hub asks for label evidence only for the trusted labels,
so the timeline lookups cost one query per labelled item, not per item.

**Configuration.** `HubSettings.trusted_gate{labels[], users[]}` and
`Repo.spec.trusted_gate{label, users[]}`. A user is
`TrustedUser{integration, login, provider_user_id}`: logins are
provider-local, so each entry names its integration and an item is checked
only against entries of the integration it came from. The label is per
integration (default `agent-ready`); a repo's own label applies to all its
items. The effective label and list are resolved at plan time (03c).

**Rule.** An item is trusted when one path holds, and no outsider edited it
after that path's trust event:

| Path | Needs | Trust event |
|---|---|---|
| Author | `author` is allow-listed | the item's `create_time` |
| Label | the trusted label is present and its latest `applied_by` is allow-listed | `applied_time` |

An "outsider edit" is `last_editor` outside the allow-list with
`last_edit_time` after the trust event. The result is `WorkItem.trust`
(`TrustVerdict{trusted, basis, granted_by, trust_time, denial}`).

| Situation | Verdict |
|---|---|
| Label applied by someone not on the list | not trusted, `NOT_ALLOW_LISTED` |
| Label present, `applied_by` unknown, or adapter lacks `labels_with_actor` / `edit_evidence` | not trusted, `EVIDENCE_MISSING` (fail closed) |
| Label removed | label path no longer holds; the author path may still |
| Trusted, then an outsider edits the body | `EDITED_AFTER_TRUST`: stage `MANUAL_ONLY`, `manual_reason = EDITED_AFTER_TRUST`; `RequeueWorkItem` refuses it like `UNTRUSTED` |
| An allow-listed person removes and re-applies the label after the edit | the label path holds again (`applied_time` is after the edit); next sync returns it to `READY` |
| A login renamed or recycled | with `provider_user_id` set the id is compared and the entry no longer matches the new owner of the login; without it the login is trusted as listed |
| A goal is already running when the edit happens | not stopped (the agent has the text); the verdict is recorded and the dispatch audit shows what was sent |

**When it runs.** The hub recomputes the verdict in the transaction that
upserts the item (so `stage` and `trust` never disagree), again at plan time
from the mirror (the `update_time` guard keeps an older poll result from
overwriting it), and again when a manual dispatch or `GetWorkItem` fetches the
body, on the `item` of **that same response**, so the text an agent gets is
the text whose editor was checked. The CONFIRM check that bypasses the gate
(03h) shows the denial.

## Failure modes added

| Failure | What happens |
|---|---|
| Adapter returns `not_modified` with no validators sent | Treated as a protocol error: logged, validators dropped, full poll |
| `meta.provider_time` differs from the hub clock by over 5 min | Logged `sync.provider_skew`; provider timestamps stay untrusted ordering evidence (03m) |
| Adapter lacks a capability the integration is configured for (upgrade) | `DEGRADED`, `CAPABILITY_MISSING`; auto-dispatch for it stops |
| `max_batch_size = 0` | The verifier sends one branch per call; the 60 s loop widens its interval accordingly |

Row 11 implements this page with three deltas. The label path is evaluated
first: a present trusted label whose apply is unusable denies at once
(`EVIDENCE_MISSING` for an unknown actor, `NOT_ALLOW_LISTED` for an
outside applier) without falling through to the author path — the table's
fail-closed reading, since the label's presence already claims a grant.
Access-lost and double-claim notices are emitted with their 03d
fingerprints (`sync.triage_raised` / `sync.triage_retired`); the
`triage_items` write lands with row 12. `by_fleet` matches the configured
fleet users, empty until row 22 wires the machine users in, so every claim
reads foreign until then; a sequential loop already runs at concurrency 1,
so a secondary limit backs off to the reset like a primary one.
