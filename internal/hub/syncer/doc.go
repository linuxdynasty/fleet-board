// Package syncer polls the provider adapters and keeps the hub's mirrors
// (docs/specs/03m-work-source-and-code-host-sync.md W4, 03j).
//
// The hub owns the polling loop and all of its state: cursors, validators,
// rate-limit state, back-off and health. Adapters keep no sync state: each
// request carries the cursor and validators, each response carries the next
// ones, the rate limits and a typed error.
//
// Work item and change request mirrors are upserted in one writer
// transaction per API response page, outside any network wait. The trust
// verdict and stage are computed in that same transaction from the mirror
// row, so they never disagree; the planner (row 23) enforces the stage.
package syncer
