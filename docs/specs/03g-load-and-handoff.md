# 03g. Load table and hand-off (moved)

- Status: Accepted (content moved; this page stays because ADRs 0013 and 0014 link to it)

The load and volume table is now the [load model in 04](04-technology-review.md#load-model-a4-and-a50). The step-4 questions were answered there, and the contract changes the workflows needed were applied in [ADR 0018](../adr/0018-hub-api-and-contract-amendments.md) and are in the protos.

The table below stays here because ADR 0018 and the protos cite its numbers ("03g change #N"). One open item stayed open: harness transcript compression (K3) is measured in v3, listed in [04, "Not proven"](04-technology-review.md#not-proven-and-how-it-is-covered).

## Required contract changes

Found while writing the workflows. All nine were applied on 2026-10-02 in
the ADR 0018 change (additive within `fleet.agent.v1` and
`fleet.integration.v1`; ADR 0017 layout). The "Applied as" column names
what is in the protos now.

| # | Change | Reason (workflow) | Applied as |
|---|---|---|---|
| 1 | `Snapshot.prompts[] PromptDetected` and `Snapshot.conditions[] HarnessCondition` (currently showing) | W2, W7: rebuild Triage from the snapshot after a reconnect; the ring is size-capped and replay is throttled, and a prompt still on screen must be visible at once | as proposed (fields 23, 24); both messages moved to `agent/v1/screen.proto` to avoid an import cycle |
| 2 | `TermDown.select{window_id \| pane_id}` on the view session (or `SelectWindow`/`SelectPane` commands scoped to an `attach_id`) | W8: the window strip and pane switcher cannot type the developer's prefix key, which the hub does not know | `TermDown.select TermSelect{window WindowKey \| pane PaneKey}`, reusing the existing key types; hub API `AttachTerminalRequest.select` |
| 3 | `MachineFacts.username` (login user on the machine) | W8: the `ssh -t <user>@<host> tmux attach -t …` line; the hub never connects, so it must be rendered from facts | as proposed |
| 4 | `HelloAck.settings_restored` (bool) | W2, W10: after a hub restore from backup the generation goes backwards; the agent must accept a lower generation once | replaced by `settings_epoch` on `Hello`, `HelloAck`, `Settings`, `SettingsAck`; settings are ordered by `(settings_epoch, generation)` (02-contracts.md) |
| 5 | `StartGoalResult.typed_goal_line` (string, 4 KiB cap) | W5, ADR 0008: the consent actually written into each goal is recorded in the audit log; `GoalState.first_line` is what the hook wrote, not what was typed | as proposed; also `PreflightResult.goal_line_preview` for the dispatch preview |
| 6 | `SetSessionAvailability.reason` (string) | W6, W8: so `AvailabilityChanged.reason` can echo `goal_merged` vs `marked_on_board` | as proposed; now `SetPaneAvailability.reason`, the enum `AvailabilityReason` (02h) |
| 7 | `RepoCheckout.goal_hooks[] {harness, GoalHooksState}` | W5: the consent form (eshu `CONSENT:` line vs consent in the objective text) depends on whether that checkout ships its own hooks; today this is only learnable through `EnsureGoalHooks` | `RepoCheckout.goal_hooks[] HarnessGoalHooks{harness, state}` (`HarnessGoalHooks` in `fleet.type.v1`) |
| 8 | In the integrations PR: `ChangeRequest.review_rounds`, `ChangeRequest.linked_work_items[]`, `ChangeRequest.head_ref`/`head_sha`, `IntegrationCapabilities{webhooks, conditional_requests, batch_query, claims, labels, merge_state}`, `WorkItemRef.kind` including `CHANGE_REQUEST` | W4, W6: review-loop cap, verification by head ref, tracker/code-host pairing, "take over a change request no goal owns" | reconciled with what #7 (ADR 0012) already merged: `linked_work_items` (field 12), `head_ref` = `source_branch`, `head_sha` = `head_commit`, `webhooks`/`conditional_requests` = `sync_modes`, `claims` = `claim_mechanisms`, `labels`, `merge_state` = `merge_detection` and `WORK_ITEM_KIND_CHANGE_REQUEST` exist and were not duplicated; only `ChangeRequest.review_rounds` and `IntegrationCapabilities.batch_query` were added; the contract review (theme F) then added `review_decision`, `CheckSummary.rollup`, `merge_state`, `merged_by`, `head_repo`, `Label` evidence, `last_editor`, cursors, validators, rate limits and `ProviderError`, and replaced `batch_query` by `max_batch_size` (03j) |
