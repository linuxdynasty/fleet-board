// Package approvals is the hub's store of owner decisions (03d W7, 02m).
//
// An approval is a decision only a person may make: answering a harness
// permission prompt, confirming a destructive command or a KillWindow on a
// developer's window, or confirming a dispatch with a CONFIRM check. The
// writer that raises the cause (the session-control RPC, row 5's ingest for
// prompts, row 22's StartGoal for dispatches) creates the approval in the
// same transaction; DecideApproval records the decision, enqueues the
// command when approved, and audits both. A PENDING approval past its
// expire_time gets the DENY fallback from the expiry job; nothing is ever
// sent for a denied or expired approval.
// Local TCP quota checkpoints run after role and decision validation, before
// a successful decision changes any row, including on a valid replay.
// ValidateListInput checks paging and filters without loading a page; the
// API can charge one read before lazy expiry and the final List query.
// USER deciders must have the required mapped role; the checked local user
// covers OWNER. Tokens and devices cannot decide.
package approvals
