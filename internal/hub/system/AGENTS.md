# internal/hub/system

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-humanizer`. Rules for this package:

- `Evaluator.Update` is pure: readings plus the clock time in, the verdict
  out. No database, no goroutine, no telemetry here; the evaluation loop and
  store slices own those. One `Evaluator` per signal lives on the hub's
  single system-evaluation goroutine.
- An unknown reading stays unknown: never fill in, smooth or carry forward
  a number. MAIN unknown is signal UNKNOWN; any other unknown measure does
  not count.
- Thresholds live in `Measures`, worked out by hand from the 05b section 8
  table, never recomputed from the code. A spec change updates `Measures`
  and its test rows together; the wiring slice reads `Measures`, so no
  level, hold or limit wording is copied anywhere else.
- New behavior gets a table-test row with the value just under and just
  over each threshold and each hold. Boundary values (exactly on a level,
  exactly the clear level) get their own rows.
- A change here updates this README and `doc.go` in the same commit.
