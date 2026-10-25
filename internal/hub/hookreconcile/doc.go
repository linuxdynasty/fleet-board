// Package hookreconcile decides when the hub sends EnsureGoalHooks
// (G1): the reconcile of 03h and 03-workflows ("Hook reconcile"), and
// the owner's EnsureMachineHooks button. It is pure: the callers (the
// gateway's post-snapshot pass, the API) supply the agent-reported
// harness and checkout states, the watched repos with their hooks_mode,
// the expected hook versions, the live-goal guard and recent sends, and
// Decide returns the wants in a stable order. Enqueueing, the request
// log and the audit stay with the callers, inside their writer jobs.
package hookreconcile
