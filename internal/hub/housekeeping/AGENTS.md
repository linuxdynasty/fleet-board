# internal/hub/housekeeping

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules for this package:

- One goroutine runs every job; a job never starts its own goroutine
  without an owner that waits for it.
- A job writes only through the store's single writer, and never holds a
  transaction across a network call.
- A failing or panicking job is logged and counted and never stops the
  others; do not add a path that returns early from a pass.
- The clock and the ticks are injected; tests never sleep for a cadence.
- A change here updates this README and `doc.go` in the same commit.
