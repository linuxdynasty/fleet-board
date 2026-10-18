// Package api is the hub's HTTP API (docs/specs/02b-hub-api.md, 02f).
//
// [NewLocalServer] serves the generated FleetService and GET /metrics on the
// local_trusted API listener. [NewMetricsServer] serves only metrics on the
// optional listener. Both check the loopback peer, Host, Origin and unsupported
// credential carriers before any handler and share [Quotas]. Checked TCP
// admission mints only the fixed USER local-owner with OWNER scopes; the
// admin socket keeps the kernel peer's distinct LOCAL_USER identity. Admitted
// TCP RPCs use configured per-class allowances after validation.
// Export success counters and logs follow read quota admission.
// Bulk list reads validate paging and reserve read quota before loading rows;
// rate-limited enrollment requests count as enrollment refusals.
// ListApprovals validates paging without reading rows before quota admission;
// it expires due approvals and reads the page afterward in one writer job.
// A refused Host or Origin is Connect permission_denied (HTTP 403), with
// ErrorDetail reason HOST_NOT_ALLOWED or ORIGIN_NOT_ALLOWED, counted in
// fleet_hub_auth_failures_total{reason="host"|"origin"}. Unsupported
// credentials and missing Connect protocol count under credentials and
// protocol; quota refusals count in fleet_hub_api_rate_limited_total with
// closed class and cause labels. No CORS header is
// ever sent. [NewServer] remains a metrics-only helper. Servers use header and
// idle timeouts and an
// ErrorLog that turns net/http's own error lines into debug-level JSON events.
//
// [Fleet] is FleetService with CreateEnrollmentToken (it writes the
// machine.enroll_token audit row with the token), the watched-repo
// writes CreateRepo, UpdateRepo and DeleteRepo (each with its repo.* audit
// row), the machine admin writes UpdateMachine, RevokeMachine and
// DeleteMachine (each with its machine.* audit row), the config import
// and export ImportConfig and ExportConfig (repos and machines apply in
// one writer transaction with a config.import audit row; other sections
// are not built yet), the hub-state triage writes SnoozeTriageItem and
// DismissTriageItem (each with its triage.* audit row), the
// session-control and approvals calls (their fresh Operation deadlines
// come from the queued commands; new session commands wake the outbox only
// after the writer commits), [Fleet.GetServerInfo],
// [Fleet.ListMachines], [Fleet.GetMachine], [Fleet.CapturePane],
// [Fleet.GetOperation], [Fleet.ListSessions], [Fleet.GetSession],
// [Fleet.ListGoals], [Fleet.GetGoal] (observed goals with the stored spec
// and status, reconciled with the row so a goal a snapshot drop ended
// reads CLOSED with its end_time), [Fleet.ListGoalRuns] (the single run 1
// off the row), [Fleet.ListGoalTransitions] (empty until the dispatch rows
// record transitions), [Fleet.ListTriageItems],
// [Fleet.GetTriageItem] (hub-state items with the stored kind, severity
// and refs), [Fleet.ListUsageReadings] (the stored reading history,
// newest first), [Fleet.ListIntegrations] (the configured integrations
// in id order), [Fleet.GetSettings], [Fleet.ListSettings] (the stored
// settings layers in id order), [Fleet.ListAuditEntries] (the stored
// audit rows, newest first), [Fleet.GetStoreStatus] (the hub's storage:
// state.db, the newest backup, free disk, start and build),
// [Fleet.GetFleetSummary] (the hub-derived strip counts),
// [Fleet.ListWorkItems],
// [Fleet.GetWorkItem] (synced mirrors with the hub's verdicts),
// [Fleet.ListChangeRequests], [Fleet.GetChangeRequest] (synced mirrors
// with the provider change request), [Fleet.ListRepos], [Fleet.GetRepo]
// (watched rows with the stored spec, status unset), [Fleet.ListAccounts],
// [Fleet.GetAccount] (owner-created accounts with the stored spec, usage
// and pacing unset) and
// [Fleet.GetSystemStatus] built; other methods are Unimplemented except
// sign-in and device calls, which are FailedPrecondition in local mode
// (02k). Implemented methods check the caller ([PrincipalFrom], ADMIN for enrollment,
// revoke/delete and store status, SETTINGS for repos, update and import, READ for
// export, the machine, operation, session, goal, work-item,
// change-request, repo, account and system reads and the
// visible capture (LOGS for its scrollback and the account raw screen), READ|APPROVALS for the triage reads and
// APPROVALS for the triage writes;
// GetServerInfo needs no scope), the fields and the request_id (02b),
// mints the token in package ca and stores only its secret hash, merges
// the masked repo patch inside the writer transaction, revokes a machine
// through a writer pre-check with a held local TCP permit, then the revocation list and writer job (stage,
// commit, post-commit reconcile, Bye), plans an import as ConfigChange
// lines behind a plan hash a later apply must repeat, keeps a machine
// entry no machine has as a pending machine for its enroll, or moves a
// triage item's state with its stamp (snooze, dismiss).
// [NewAdmin] serves it on the admin socket over HTTP/1.1,
// closing unserved every connection whose kernel peer user id
// ([KernelPeerUID]) is not fleetd's and refusing any request with an Origin
// header; the caller is [LocalUser] of the peer. [AdminClient] dials the
// socket. Watch remains unimplemented until its plan-row proof is complete.
package api
