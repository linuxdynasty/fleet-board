# internal/hub/statedb

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store` and `fleet-security`. Rules for this package:

- Every write goes through the store's single writer (`Write` or `Submit`).
  Nothing here opens its own write connection.
- `Migrations()` is append-only: never edit a shipped entry (the store's
  checksum check refuses it). Tables belong to the rows that need them; add
  them here in order, each with the spec change that defines it.
- state.db never exists without `hub_identity`, and nothing but `Create`
  makes state.db. `Open` never creates a file.
- Never loosen a mode check, never chmod a folder or file the owner made, and
  never log a field of the identity row (CA key, server key, salt).
- `Hello` never changes `machines.state`; only the Snapshot transaction
  marks a machine `ONLINE` (row 5 ruling, fork 1). The machine id and serial
  a connection writes come from the verified certificate, never from a
  message.
- Tests use real files in `t.TempDir()` and check modes, hashes and rows on
  the file itself.
- A change here updates this README and `doc.go` in the same commit.
