// Package goals tracks the goals the owner starts by hand (E9): it finds
// their eshu goal files in pane checkouts, reports each as an observed
// goal with an agent-minted id, and ends it on DONE or pane exit. The hub
// shows it and never dispatches, nudges or controls it (02h "Observed
// goals", 03k). Fleet-started goals arrive with row 21; until then the
// registry seam answers unbound for every pane.
//
// The condition observer (conditions.go, 01 E12) re-reads each live goal's
// pane screen with its harness's own condition parser, emits
// HarnessCondition events with cleared pairs, and lists the open ones in
// Snapshot.conditions with the goal id from the tracker. A throttling
// condition carries the retry detail (retrying, attempt, status, reset)
// for the hub's limit wait (03h).
package goals
