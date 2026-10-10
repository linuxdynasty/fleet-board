# internal/hub/lock

The guard that keeps two `fleetd` processes off one state folder (#92). It is
an exclusive, non-blocking `flock` on `<state_dir>/fleetd.lock`.

```go
l, err := lock.Acquire(filepath.Join(stateDir, "fleetd.lock"))
if errors.Is(err, lock.ErrHeld) { /* another fleetd runs here: refuse to start */ }
defer l.Release()
```

- `fleetd init`, `fleetd run` and `fleetd restore` take it before they touch
  `state.db`.
  `fleet-agent run` takes `<state-dir>/fleet-agent.lock` the same way before
  it opens `agent.db`, and says "another fleet-agent run" instead of
  `ErrHeld`'s text, which names fleetd. With two users the package belongs
  outside `internal/hub`; moving it (and making `ErrHeld` name no program)
  is left for a later change.
- A held lock is `ErrHeld` at once; nothing waits.
- The file is mode 0600 and holds the holder's pid, for a person to read. The
  pid decides nothing: the kernel drops the lock when the process exits, even
  on SIGKILL, so a leftover file never blocks a start.
- `flock` locks belong to one open of the file, so a second `Acquire` in the
  same process is refused too.
- `Release` unlocks (`LOCK_UN`) before it closes. A child that another
  goroutine forked shares the open file until it starts its program, so a
  close alone could leave the lock held for that moment.

Tests: `CGO_ENABLED=0 go test ./internal/hub/lock/`.
`TestAnotherProcessHoldsTheLockUntilItDies` runs a child process that holds
the lock, checks the parent is refused, kills the child with SIGKILL and checks
the parent then gets the lock.
