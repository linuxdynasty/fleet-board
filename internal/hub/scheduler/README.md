# scheduler

The row-23 auto-dispatch planner: one 30 s pass over READY work items,
ranked accounts and fit machines (03c "Planning rules").

Pure: `Plan` reads a `Snapshot` and returns a `Decision`. The
`FleetService` runner builds the snapshot (trust recomputed from the
mirror, pacing from the latest reading), commits each decision through
the dispatch write path, and records the pass metrics. `GatePass` owns
the 30 s cadence; `CheckRequeue` validates `RequeueWorkItem`.
