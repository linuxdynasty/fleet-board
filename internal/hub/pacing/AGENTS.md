# internal/hub/pacing

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-humanizer`. Rules for this package:

- `Evaluate` is pure: settings plus the latest reading in, the verdict out.
  No database, no clock beyond the `now` argument, no telemetry here; the
  scheduler and store slices own those.
- An unknown reading stays unknown: never fill in, smooth or carry forward
  a number. A replayed lower percent is the new truth, not an error.
- Precedence is EXCLUDED, UNKNOWN (no reading, parse failed), STALE,
  RESERVE, SHORT_LIMIT, UNKNOWN for the line, then the bands. RESERVE beats
  SHORT_LIMIT so the alert fires; a concrete SHORT_LIMIT beats an unknown
  line. Changing the order needs a spec reason and a test.
- New behavior gets a table-test row worked out by hand from 03b, not
  recomputed from the code. Boundary values (+5, +10, -5, 95, 24 h) get
  their own rows.
- A change here updates this README and `doc.go` in the same commit.
