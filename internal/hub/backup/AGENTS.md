# internal/hub/backup

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules
for this package:

- A backup holds the CA key and the account salt: it is a secret. Every file
  this package makes is created `O_EXCL` with mode 0600 before anything is
  written to it, in a 0700 folder. Never widen a mode, never chmod a folder
  the owner made.
- Copies come only from `store.VacuumInto` (its own `mode=ro` connection).
  Never copy through `Store.Read` (`VACUUM INTO` fails on `query_only`),
  never through the writer, and never take `fleetd.lock`.
- A copy is checked before it is published, and publishing never replaces a
  file (`os.Link`). Every temporary file, above all the plaintext of an
  encrypted copy, is removed on every return path, success and failure; a
  crash leaves them for `RemoveLeftovers`, which must keep its age limit
  because `fleetd backup` takes no lock.
- The passphrase comes only from a terminal (`Terminal`), asked twice; never
  from a flag, an environment variable, a file or a pipe. Never log it, never
  keep it past `Take`, never put file contents or paths in a log field.
- Pruning touches only `nightly-*.db`; a manual copy is never deleted.
- The clock and the ticks are injected; tests never sleep for a schedule.
- Tests use real files in `t.TempDir()` and a real `state.db`, and check
  modes, names and the copy's rows on the files themselves.
- A change here updates this README and `doc.go` in the same commit.
