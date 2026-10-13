package api

import (
	"errors"

	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

// SecretHeader carries the local approval secret in local_trusted mode
// (02f): the hex secret, kept in the dashboard login session, never in the
// shell or a file. Fleet and phone clients send it on DecideApproval and
// on the SETTINGS and ADMIN calls; the hub compares it in constant time
// against the file beside state.db.
const SecretHeader = "X-Fleet-Approval-Secret"

// requiresLocalSecret is the exact call list of ADR 0032, decision 1 and
// 2: in local_trusted mode these calls need the local approval secret as
// well and fail without it, even on loopback. Keys are the Connect
// procedure names. A new SETTINGS or ADMIN RPC adds its row here with its
// denied-path test; fleet-code-review checks the 02d scope table against
// this list.
var requiresLocalSecret = map[string]bool{
	// Approving a request.
	"/fleet.hub.v1.FleetService/DecideApproval": true,
	// Changing settings: every SETTINGS call (02d).
	"/fleet.hub.v1.FleetService/UpdateSettings":     true,
	"/fleet.hub.v1.FleetService/ImportConfig":       true,
	"/fleet.hub.v1.FleetService/CreateRepo":         true,
	"/fleet.hub.v1.FleetService/UpdateRepo":         true,
	"/fleet.hub.v1.FleetService/DeleteRepo":         true,
	"/fleet.hub.v1.FleetService/CreateAccount":      true,
	"/fleet.hub.v1.FleetService/UpdateAccount":      true,
	"/fleet.hub.v1.FleetService/DeleteAccount":      true,
	"/fleet.hub.v1.FleetService/UpdateIntegration":  true,
	"/fleet.hub.v1.FleetService/CreateRoutine":      true,
	"/fleet.hub.v1.FleetService/UpdateRoutine":      true,
	"/fleet.hub.v1.FleetService/DeleteRoutine":      true,
	"/fleet.hub.v1.FleetService/UpdateMachine":      true,
	"/fleet.hub.v1.FleetService/EnsureMachineHooks": true,
	// Changing settings: every ADMIN call (02d).
	"/fleet.hub.v1.FleetService/DrainMachine":             true,
	"/fleet.hub.v1.FleetService/UndrainMachine":           true,
	"/fleet.hub.v1.FleetService/RevokeMachine":            true,
	"/fleet.hub.v1.FleetService/DeleteMachine":            true,
	"/fleet.hub.v1.FleetService/CreateEnrollmentToken":    true,
	"/fleet.hub.v1.FleetService/ApprovePairing":           true,
	"/fleet.hub.v1.FleetService/CreateUserInvite":         true,
	"/fleet.hub.v1.FleetService/UpdateUser":               true,
	"/fleet.hub.v1.FleetService/CreateApiToken":           true,
	"/fleet.hub.v1.FleetService/RevokeApiToken":           true,
	"/fleet.hub.v1.FleetService/DeletePasskey":            true,
	"/fleet.hub.v1.FleetService/RevokeLoginSession":       true,
	"/fleet.hub.v1.FleetService/UpdateNotificationTarget": true,
	"/fleet.hub.v1.FleetService/GetStoreStatus":           true,
}

// RequiresLocalSecret reports whether procedure needs the local approval
// secret in local_trusted mode. The 02d scope table is the human copy;
// this map is the required list. Enforcement is per-RPC: each handler
// calls checkLocalSecret, so a new SETTINGS or ADMIN RPC must add its own
// call and denied-path test with it.
func RequiresLocalSecret(procedure string) bool {
	return requiresLocalSecret[procedure]
}

// checkLocalSecret enforces the secret for DecideApproval (and, as their
// rows land, the SETTINGS and ADMIN calls): in local_trusted mode the
// presented header must match the file, or the call fails closed. Outside
// local_trusted mode the signed-in credential covers it and no secret is
// needed.
func (f *Fleet) checkLocalSecret(procedure, presented string) error {
	if !f.localTrusted || !RequiresLocalSecret(procedure) {
		return nil
	}
	if f.stateDir == "" {
		return errors.New("api: no state dir to check the approval secret against")
	}
	return secret.Check(f.stateDir, presented)
}
