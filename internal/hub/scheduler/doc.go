// Package scheduler is the row-23 auto-dispatch planner (03c "Planning
// rules", one pass every 30 s).
//
// The planner is pure: a Snapshot in, a Decision out. It reads no store,
// clock or network and owns no telemetry; the caller (the FleetService
// runner) builds the snapshot from the store — recomputing the trusted
// gate from the mirror and the pacing verdict from the latest reading at
// plan time — commits each decision through the dispatch write path, and
// records the pass metrics. Purity keeps the whole 03c rule table under
// table tests with values worked out by hand from the spec.
//
// What the planner decides, per 03c planning rules 1 to 4:
//
//   - Work items: stage READY, trusted (recomputed), unclaimed, no live
//     goal, repo watched and below its cap; oldest hub create_time first.
//   - Accounts: pacing OK, BEHIND or AHEAD with active goals below the
//     allowed count; ranked by waste (most unused quota first). RESERVE,
//     SHORT_LIMIT, UNKNOWN, STALE and EXCLUDED never dispatch.
//   - Machines: ONLINE, not draining, no fresh unfit marker for the
//     (harness, repo) scope, the account's harness launch-allowed, under
//     both slot caps; non-hub first, then most free active slots.
//   - Flood guard: at most one dispatch per account and per machine per
//     pass; at most one pass per 30 s (GatePass).
//
// What the planner deliberately does not decide here (no store backing in
// this PR): pane reuse (no pane inventory table; the agent's preflight
// picks a new window and fails SESSION_NOT_AVAILABLE instead of reusing
// blindly), forge push access, checkout presence and account sign-in (the
// agent's preflight refuses those with BLOCK, 03c failure modes). The
// commit transaction re-checks the mirror's claims and the live-goal
// unique indexes, so a pass that raced a claim or a second decision loses
// cleanly (03f "What can race") and retries next pass.
package scheduler
