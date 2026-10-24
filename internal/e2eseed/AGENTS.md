# internal/e2eseed

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Thin wiring only: parse the one argument, call `internal/seed`, report
  the exit code. It never starts a process and never touches the network.
- Deterministic only: no clock reads, no random bytes. Seeding twice fails;
  the shared fleet is never silently replaced.
- A change here updates this README and `doc.go` in the same commit.
