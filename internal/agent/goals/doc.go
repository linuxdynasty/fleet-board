// Package goals tracks the goals the owner starts by hand (E9): it finds
// their eshu goal files in pane checkouts, reports each as an observed
// goal with an agent-minted id, and ends it on DONE or pane exit. The hub
// shows it and never dispatches, nudges or controls it (02h "Observed
// goals", 03k). Fleet-started goals arrive with row 21; until then the
// registry seam answers unbound for every pane.
package goals
