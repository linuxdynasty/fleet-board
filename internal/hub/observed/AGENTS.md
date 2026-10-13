# internal/hub/observed

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Observed goals only (03k): rows the agent reports with binding
  OBSERVED. Fleet-bound goals are row 21's; this applier skips any other
  binding without touching it.
- The spec is written once and never rewritten, except the harness an
  event cannot see (empty to known). Later sightings replace the status
  only; an unchanged sighting writes nothing and takes no stamp.
- Phases come from `PhaseFor` and nothing else; the table is the one in
  [03k](../../../docs/specs/03k-observed-goals-and-repos.md#what-the-hub-stores).
- SQL lives in `internal/hub/statedb` (`goals.go`); this package builds
  the spec and status and drives the applier order. A change here
  updates this AGENTS.md and `doc.go` in the same commit.
