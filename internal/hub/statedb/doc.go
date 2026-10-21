// Package statedb owns the hub's state folder and state.db: the folder and
// file modes, the ordered state.db migrations, and the hub_identity row
// (docs/specs/02j-hub-runtime.md, 02g, 03f).
//
// [Create] builds state.db so that it never exists without its hub_identity
// row: a temporary file in the folder (mode 0600 before SQLite opens it) is
// migrated and gets the row through the store's single writer, is closed
// (checkpointing the WAL away), and is then linked to state.db, which fails
// when state.db exists ([ErrExists]); a rename would replace it. A folder
// sync failure after the link is [ErrCreatedNotSynced], and [RemoveLeftovers]
// clears a crashed Create's temporary files. [Open] opens an existing state.db only
// (store.ErrMissing otherwise) after [CheckDir] and the file checks refuse a
// folder or file open to other users ([ErrLooseMode]). [ReadIdentity] reads
// the row ([ErrNoIdentity] when it is missing) and [ReplaceServerCert] swaps
// the server certificate in one writer transaction. [InsertEnrollToken]
// stores an enroll_tokens row (the SHA-256 of a token's secret, never the
// secret) after the request_id checks ([ErrSecretDelivered],
// [ErrRequestConflict]) and the re-enroll check ([ErrNotReenrollable]), all
// in one writer transaction.
//
// Plan row 3 adds machines, cert_serials and enroll_responses. [CheckEnroll]
// is the read-pool pre-check in the 02g order ([ErrTokenUnknown], a replay
// or [ErrTokenUsed], [ErrTokenExpired]); [Enroll] is
// the one writer job that burns the token and inserts the machine, its
// serial and the replayable response, or classifies again when the burn
// changes nothing. [RevokeSerials] and [RevokedSerials] keep the revocation
// list in state.db; the revoked-serials file is the revocation package's.
//
// Plan row 4 adds cert_renewals, unique on the machine and the CSR key.
// [Renew] refuses a missing or REVOKED machine ([ErrMachineUnknown],
// [ErrMachineRevoked]) and an unknown or revoked peer serial
// ([ErrPeerSerialUnknown]), returns the stored certificate for the same
// machine and key, or signs and inserts the serial and the renewal in one
// writer job.
//
// Plan row 5 (PR 5a-i) adds machine_connections and the machine's
// last_epoch and last_seen_time. [Hello] is one writer job: a live machine
// row and a serial that is not revoked ([ErrSerialRevoked]; unlike [Renew],
// a serial the store lacks is accepted), then epoch = max(stored,
// Hello.last_epoch) + 1 ([ErrEpochOutOfRange] past [MaxEpoch]), the
// machine's live row closed as displaced and returned, the new live row,
// and never a change of machines.state (the Snapshot transaction marks
// ONLINE). [EndConnection]
// closes one epoch's row if it is still live and reports whether it did;
// in the same job the live row's close by the stream or the agent takes an
// ONLINE or STALE machine OFFLINE (plan row 5b), so a displaced stream
// never changes its machine.
//
// Plan row 5b adds the machine state changes: [MarkState] (housekeeping's
// STALE and OFFLINE, only while the state and the live epoch are what it
// judged), [HeartbeatSeen] (last_seen, and STALE back to ONLINE),
// [StartSweep] (rows left live closed as hub_restart, ONLINE made STALE)
// and [MachineStates] (the housekeeping read). Each change is stamped and
// returned as a [StateChange]; none writes an audit row.
//
// Plan row 5 (PR 5d) adds hub_counters, audit_log and the Watch stamp on
// machines. [NextStamp] takes the next hub_seq inside a writer job, rolled
// back with it on failure; [AppendAudit] stamps and inserts an audit row in
// the same transaction as the change it records, and [WriteAudit] writes
// rows as a job of their own. [InsertEnrollToken], [Enroll] and [Renew]
// each append their row (machine.enroll_token, machine.enroll,
// machine.cert_renewed); a replay writes none. Audit rows are permanent.
//
// Plan row 5 (PR 5j) adds hub_identity.restore_reconcile_until (the
// restore_reconcile migration).
// [ApplyRestore] is fleetd restore's one writer transaction on a restored
// store: settings_epoch, hub_addresses, the server certificate when an
// address was added, the reconcile window and the hub.restore audit row
// ([ActionRestore]). [RemoveLeftovers] also
// clears a crashed restore's temporary file ([RestoreTempPrefix]).
//
// Plan row 5 (PR 5c1) adds the Snapshot columns on machines
// (machine_snapshot migration): hub_last_event_seq, event_generation, the
// EventCounters, the seq floor and its epoch, resources, policy and the
// reported drain. [ApplySnapshot] is the core of the Snapshot's writer job:
// the current-epoch check ([ErrSnapshotNotCurrent], [ErrSnapshotInvalid]),
// agent rollback on the epoch's first Snapshot only (a new
// event_generation and a machine.install_changed row,
// [ActionInstallChanged]) and the stored values; [MarkOnline] ends the job
// after the later rows' appliers and reports its state change
// ([ReasonSnapshot]); [ReadSnapshot] reads them back. [Hello]
// returns hub_last_event_seq.
//
// Plan row 12 adds the machine reads: [MachineRow] is one machines row as
// stored; [ReadMachineRow] reads one by id ([ErrMachineUnknown] without a
// row) and [ListMachineRows] reads them all, both on the read pool for the
// hub API's filter, sort and page.
//
// Plan row 12 (session reads) adds the create-time session record
// (hub_session_details migration): [SessionRow] is one hub_sessions row
// with the name, server and owner its CreateSession stored;
// [ReadSessionRow] reads one by id ([ErrSessionUnknown] without a row)
// and [ListSessionRows] reads them all, both on the read pool.
//
// Plan row 12 (goal reads) adds the observed-goal read path over the row
// 9 goals table: [GoalRow] is one goals row with the stored spec, status,
// phase, reason and times; [ReadGoalRow] reads one by id ([ErrGoalUnknown]
// without a row) and [ListGoalRows] reads them all, both on the read pool
// for the hub API's filter, sort and page.
//
// Plan row 12 (work-item reads) adds the mirror read path over the row 11
// work_items table: [WorkItemRow] is one mirror row with the provider
// item, the hub's verdicts, the version and the times; [ReadWorkItemRow]
// reads one by canonical key ([ErrWorkItemUnknown] without a row) and
// [ListWorkItemRows] reads them all, both on the read pool for the hub
// API's filter, sort and page.
//
// Plan row 12 (change-request reads) adds the mirror read path over the
// row 11 change_requests table: [ChangeRequestRow] is one mirror row with
// the provider change request, the version and the times;
// [ReadChangeRequestRow] reads one by canonical key
// ([ErrChangeRequestUnknown] without a row) and [ListChangeRequestRows]
// reads them all, both on the read pool for the hub API's filter, sort
// and page.
//
// Plan row 12 (repo reads) adds the watched-repo read path over the row
// 13a repos table: [GetWatchedRepo] reads one by host/path
// ([ErrRepoUnknown] without a row) and [ListWatchedRepoRows] reads them
// all, both on the read pool for the hub API's filter, sort and page.
//
// Plan row 12 (account reads) adds the owner-account read path over the
// row 13a accounts table: [GetAccount] reads one by id
// ([ErrAccountUnknown] without a row) and [ListStoredAccounts] reads
// them all, both on the read pool for the hub API's filter, sort and
// page.
//
// Plan row 12 (triage reads) adds the hub-state triage read path over
// the row 13a triage table: [GetStoredTriageItem] reads one by id
// ([ErrTriageUnknown] without a row) and [ListStoredTriageItems] reads
// them all, both on the read pool for the hub API's filter, sort and
// page.
//
// Plan row 12 (usage reads) adds the reading history (usage_readings
// migration): [InsertUsageReading] appends one reading keyed by
// (account, read_time, received_time) with no audit row, and
// [ListStoredUsageReadings] reads them all on the read pool for the hub
// API's filter, sort and page. Plan row 13 adds the bounded
// [ListStoredUsageReadingsSince] scan (read_ms index) for the recurring
// rollup and insights passes, plus the account_reserve_history table:
// [ReservePercentOfSpec] reads the current reserve (default 10, cap 50)
// and [ListAccountReserveHistory] its effective-time history.
//
// Plan row 12 (integration reads) adds the configured-integration read
// path over the integrations table: [EnsureIntegration] and
// [SetIntegrationStatus] own the rows, and [ListStoredIntegrations]
// reads them all on the read pool for the hub API's filter, sort and
// page.
//
// Plan row 12 (settings reads) adds the settings-layer read path over
// the settings_layers table: [UpdateFleetSettings] and the import and
// enroll own the rows, and [ListSettingsLayers] and [ReadSettingsLayer]
// read them on the read pool for the hub API's filter, sort and page.
//
// Plan row 12 (audit reads) adds the audit-log read path over the
// audit_log table: every writer job appends its rows with [AppendAudit],
// and [ListStoredAuditEntries] reads them all on the read pool for the
// hub API's filter, sort and page.
//

// Plan row 5 (PR 5c2) adds the event history (events migration):
// [ApplyEvent] stores one event in its writer job (the live, snapshotted
// epoch, [ErrEventNotCurrent]; the stream's chain, [ErrEventChainBroken];
// a re-send is [EventKnown]; history only at or below snapshot_seq) and
// moves hub_last_event_seq; [AppendMarker], [RecordEventLoss] and
// [PruneEvents]. The Snapshot core writes the gap and state_reset markers.
//
// Plan row 5 (PR 5f) adds commands (the outbox row and its Operation in
// one, unique per command_id, a send unique per machine, epoch and seq)
// and command_results (the first result, never changed). The queries are
// internal/hub/outbox's.
//
// Plan row 13a adds the owner repo writes (repo_request_log migration):
// [CreateWatchedRepo], [UpdateWatchedRepo], [UpdateWatchedRepoMasked] and
// [DeleteWatchedRepo] stamp the repos row they change (hub_seq), keep the
// idempotency record (the stored response, replayed for 24 h, expired rows
// pruned in bounded batches) and write the repo.* audit row, all in one
// writer transaction. [UpdateWatchedRepoMasked] merges the masked patch
// against the transaction-current row. [DeleteWatchedRepo] moves the repo's
// READY mirrors to SEEN in the same transaction. [LookupRepoReplay] is the
// read-pool pre-check behind a write.
//
// Plan row 13a (slice 5) adds the hub-state triage writes
// (triage_hub_writes migration): [SnoozeTriageItem] and
// [DismissTriageItem] move an OPEN or SNOOZED triage_items row at version
// + 1 with its hub_seq stamp, keep the idempotency record (the acted item
// as JSON, replayed for 24 h, expired rows pruned in bounded batches) and
// write the triage.snooze or triage.dismiss audit row, all in one writer
// transaction. A closed item is [ErrTriageClosed] before any version
// check; a stale expected_version is a [VersionStale]; a past wake time
// is [ErrTriagePastUntil], checked after the replay lookup.
//
// Plan row 13a (issue #40) stamps the owner machine and account deletes:
// [DeleteMachine] and [DeleteOwnerAccount] take the next hub_seq after
// the row is gone and return the last state with it (the machine delete
// also returns the full list row), so Watch replays the DELETED after
// every earlier change; a replayed request_id answers the stored
// outcome.
//
// Plan row 13 (issue #41) adds the SYSTEM item writer: [ListOpenSystemItems]
// reads the open SYSTEM rows as the open list behind system.Decide, and
// [ApplySystemChanges] persists one round of Decide writes in one writer
// transaction (raises at version 1 with a 7-day expiry, updates in place at
// version + 1 keeping OPEN or SNOOZED, retires closed clearing the snooze).
// A row that left the open states since the round was decided is skipped;
// the next round re-decides from the fresh open list. Like the other triage
// raises it writes no audit row.
//
// Plan row 13a (issue #48) adds the config import (config_import_requests
// migration) and the pending machines (pending_machines migration):
// [ApplyConfigImport] applies a verified plan in one writer transaction
// (the replay check, every repo, machine and pending op, the config.import
// audit row and the idempotency record, or nothing). A machine op carries
// the baseline version ([ListMachineIdentities] reads it) and the apply
// refuses a machine an owner write moved since the dry run
// ([ErrImportStale]). [ListPendingMachines] reads the kept entries;
// enroll consumes the matching one (merging its labels, draining the
// machine, storing its values as the layer), a rename onto a kept name
// leaves the shadow the next import heals.
// [WithValidatedWrite] lets local TCP config, enrollment, machine and triage
// owner writes reserve quota in that writer job after replay and mutable checks,
// before the first change. A denied check rolls back the whole job. Revoke
// reserves in [PrecheckRevokeForWrite] before its separate file stage and
// rechecks the row in the final writer job.
//
// Plan row 12 (Watch ingest slice) adds the publish stamps: the
// hub_session_watch_stamp migration stamps each hub-started session's create
// write (hub_sessions.hub_seq, read as [SessionRow.HubSeq]; 0 on older rows,
// which never publish as live), and the mirror_watch_stamps migration stamps
// each mirror's last real change (work_items.hub_seq and
// change_requests.hub_seq, read as [WorkItemRow.HubSeq] and
// [ChangeRequestRow.HubSeq]). Every stamped write takes its number with
// [NextStamp] inside its committing job. [RestampMachine] and [RestampGoal]
// move one row to a fresh stamp for the volatile flush; [ReadGoalTx] reads a
// goal inside the writer job for applier after-images, and [EndObservedGone]
// returns the ids it closed for the same publish.
//
// Plan row 11 adds the provider mirrors (provider_sync_mirrors migration):
// work_items and change_requests keyed canonically with the hub's verdicts
// beside the provider shape, sync_cursors and rate_limit_state per page
// and budget, and the integrations and repos rows. [UpsertWorkItemPage]
// and [UpsertChangeRequestPage] commit one API page with its cursors and
// budgets under the update_time guard; [UpdateWorkItemClaims] is the claim
// loop's column with its stage recompute; [GetCursor],
// [GetWorkItemStages], [GetWorkItemState] and [ListClaimCandidates] are the
// loop's reads; [EnsureIntegration], [GetIntegration] and
// [SetIntegrationStatus] own the integration row; [UpsertRepo] keeps the
// sync-owned repo fields and [ListWatchedRepos] reads them, while the owner
// writes above own the row once watched.
package statedb
