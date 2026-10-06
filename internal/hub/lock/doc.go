// Package lock is the one-hub-per-state-folder guard (#92):
// <state_dir>/fleetd.lock, held with an exclusive, non-blocking flock.
//
// `fleetd init` and `fleetd run` call [Acquire] before they open state.db, so
// two hubs never write one store. A lock that is held is [ErrHeld] at once,
// never a wait. The kernel releases the lock when the holder exits, even on
// SIGKILL, so a file left behind never blocks the next start. [Lock.Release]
// unlocks before it closes, so a forked child that still shares the open file
// does not keep the lock held. Unix only.
package lock
