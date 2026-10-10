# internal/hub/auditcap

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- No I/O, no goroutines, no timers: it decides, the caller writes. The lock
  is held only for the in-memory decision.
- Every map stays bounded by `MaxTracked`; a flood from many addresses must
  not grow memory.
- The caps are the defaults.md row; change both together.
- Test through `New`, `Note` and `Flush` with explicit times, never the
  wall clock.
- A change here updates this README and `doc.go` in the same commit.
