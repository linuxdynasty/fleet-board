# tools/telemetrynames

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Standard library only (`go/parser`, `go/ast`, `go/token`), no cgo, no type
  checker: it must run on a fresh checkout without `make gen`.
- It is a gate: a name it cannot read as a static string fails, it never
  guesses. Doc-to-code stays a report (exit 0) until plan row 17.
- The map lives in `.agents/telemetry-docs.json` and points only at
  `docs/specs/`; fix a finding by documenting the name in that spec or mapping
  its area there, not by adding an exception to the tool.
- Test through `Check` and `run` with fixture code and specs in a temporary
  folder; a new rule gets a table case that fails without it.
- A change here updates this README and `doc.go` in the same commit.
