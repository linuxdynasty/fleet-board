# internal/ulid

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`. Rules for
this package:

- Standard library only, no cgo, no global state: callers pass `now` and the
  random source, so tests set both.
- The output format is a contract (ids.proto): 26 lowercase Crockford
  characters. Changing it changes every stored id; it needs an ADR.
- Tests compare with vectors from the ULID spec or worked out by hand,
  never with values the code computes.
- A change here updates this README and `doc.go` in the same commit.
