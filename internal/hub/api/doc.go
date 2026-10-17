// Package api is the hub's HTTP API (docs/specs/02b-hub-api.md, 02f).
//
// [Handler] is the whole surface of the local_trusted API listener (and of
// the optional metrics listener): the 02f Host and Origin checks before any
// handler, then GET /metrics, and 404 for every other path. A refused Host or
// Origin is a Connect permission_denied error (HTTP 403) whose ErrorDetail
// reason is HOST_NOT_ALLOWED or ORIGIN_NOT_ALLOWED, counted in
// fleet_hub_auth_failures_total{reason="host"|"origin"}. No CORS header is
// ever sent. [NewServer] wraps it with header and idle timeouts and an
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
// session-control and approvals calls, [Fleet.GetServerInfo],
// [Fleet.ListMachines], [Fleet.GetMachine], [Fleet.CapturePane],
// [Fleet.GetOperation], [Fleet.ListSessions], [Fleet.GetSession],
// [Fleet.ListGoals], [Fleet.GetGoal] (observed goals with the stored spec
// and status, reconciled with the row so a goal a snapshot drop ended
// reads CLOSED with its end_time), [Fleet.ListWorkItems],
// [Fleet.GetWorkItem] (synced mirrors with the hub's verdicts) and
// [Fleet.GetSystemStatus] built; every other method is Unimplemented
// (02k): it checks the caller ([PrincipalFrom], ADMIN for enrollment
// and revoke/delete, SETTINGS for repos, update and import, READ for
// export, the machine, operation, session, goal, work-item and system reads and the
// visible capture (LOGS for its scrollback), APPROVALS for triage;
// GetServerInfo needs no scope), the fields and the request_id (02b),
// mints the token in package ca and stores only its secret hash, merges
// the masked repo patch inside the writer transaction, revokes a machine
// through the revocation list and the writer job (pre-check, stage,
// commit, post-commit reconcile, Bye), plans an import as ConfigChange
// lines behind a plan hash a later apply must repeat, keeps a machine
// entry no machine has as a pending machine for its enroll, or moves a
// triage item's state with its stamp (snooze, dismiss).
// [NewAdmin] serves it on the admin socket over HTTP/1.1,
// closing unserved every connection whose kernel peer user id
// ([KernelPeerUID]) is not fleetd's and refusing any request with an Origin
// header; the caller is [LocalUser] of the peer. [AdminClient] dials the
// socket. Watch and FleetService on the API listener arrive with their plan
// rows.
package api
