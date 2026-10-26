// Package preflight runs the agent's I1 Preflight (plan row 21): the five
// read-only checks StartGoal step 1 runs before anything is typed
// (HARNESS_NOT_SIGNED_IN, PUSH_ACCESS_MISSING, CHECKOUT_MISSING,
// FOLDER_UNTRUSTED, DISK_LOW). Each failure comes back as its named reason
// in PreflightResult.failed_checks, and no check types a key on any path:
// the checker holds no sender, only a Reader seam and a counting Typer the
// tests watch stay at zero.
//
// would_reuse and goal_line_preview stay unset here; the reuse-candidate
// search (E11) and the consent-line render land with later row-21 slices,
// and the coordinator wires the production Reader.
package preflight
