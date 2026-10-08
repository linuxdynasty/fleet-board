# internal/hub/revocation

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules
for this package:

- The order is the contract: the file is written and fsynced before the
  in-memory set changes, and the set before `state.db`. Never reorder it;
  a revoke must fail closed.
- `Revoked` never blocks and never does I/O: it reads one atomic pointer.
  Never mutate a published map; build a new one and swap it.
- The file format is in 02g. A format change needs a new header version and
  a spec change in the same PR. Never loosen the mode check and never chmod
  the file.
- Writes to `state.db` go through `statedb` (the store's single writer).
- Errors name a line number, never the line; logs carry no serial list.
- Tests use real files in `t.TempDir()` and a real `state.db`, and compare
  the file text with text written out by hand.
- A change here updates this README and `doc.go` in the same commit.
