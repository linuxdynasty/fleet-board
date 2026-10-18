# internal/seed

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Deterministic only: no clock reads, no random bytes, no real
  certificates. Every stamp comes from the fixed seed clock and every
  secret is a fixed test vector, so two seeds hold the same rows.
- Build through the public store paths (`statedb.Create`, `Enroll`,
  `Hello`, `ApplySnapshot`); never write SQL against `state.db` here, so
  a schema change breaks this package the way it breaks production.
- Keep the fleet tiny (two machines) until a row's tests need more; each
  addition names the row that needs it.
- A change here updates this README and `doc.go` in the same commit.
