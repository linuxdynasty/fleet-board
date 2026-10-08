# internal/protoversion

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-contract-rigor`. Rules for this package:

- Constants and one pure function only; standard library only, no cgo.
- Hub and agent both import this package; never copy the range elsewhere.
- Raising `Max` or `Min` is a wire change: update 02l and the support rule
  of ADR 0003 in the same PR.
- A change here updates this README and `doc.go` in the same commit.
