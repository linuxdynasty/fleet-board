# internal/hub/lock

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Never block: the lock is taken with `LOCK_NB`, and a held lock is `ErrHeld`.
  A start that waits for another hub hides a second hub instead of refusing it.
- The pid in the file is for people. Never read it to decide whether a hub is
  alive; only the kernel's flock decides.
- Unix only (`//go:build unix`), standard library only, no cgo.
- Test through `Acquire` and `Release`, with a real file and a real child
  process for the cross-process cases.
- A change here updates this README and `doc.go` in the same commit, and the
  `fleetd.lock` row of 02j when the behavior changes.
