# cmd/fleetd

Read the root `AGENTS.md` first; it applies here. Rules for this directory:

- `main.go` is wiring only: parse flags, build dependencies, call into
  `internal/`. Logic that needs a test belongs in `internal/`.
- No cgo; the binary must build with `CGO_ENABLED=0`.
- The version comes from `internal/buildinfo` and the linker; never hard-code
  one here.
- A change here updates this README and `doc.go` in the same commit
  (`scripts/check-docs-stale.py` reminds you).
