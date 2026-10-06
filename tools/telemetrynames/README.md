# tools/telemetrynames

The `make telemetry-names` gate: every log event and metric name in the Go code
is written down in the spec that owns it. Specs are the source of truth, never
a package README.

- What it reads: non-test `.go` files under `code_dirs` (`cmd/`, `internal/`),
  skipping `gen/`, `testdata/` and `vendor/`. Log events are the message
  argument of slog calls (`log.Info("store.opened", ...)`,
  `x.tel.Logger.WarnContext(ctx, "admin.peer_refused", ...)`), also across
  several lines. Metrics are the name of a meter instrument constructor
  (`m.Int64Counter(prefix+"_writer_jobs_total", ...)`); for `prefix + "_tail"`
  the tail is checked as `fleet_hub_<tail>` or `fleet_agent_<tail>`.
- The map: [`.agents/telemetry-docs.json`](../../.agents/telemetry-docs.json)
  sends each log area (`enroll.`) and metric tail prefix (`writer_`, longest
  match wins) to one spec under `docs/specs/`.
- It fails (exit 1), naming `file:line`, when a name is not a static string,
  its area is unmapped, or its spec does not hold it as a whole token (a name
  only another spec lists still fails). A bad config, a missing spec or code
  folder, or a parse error is exit 2.
- Report only (exit 0): names a spec lists for its mapped areas that no code
  uses yet (first 15; `-all` lists every one), until plan row 17.

Run: `make telemetry-names` (builds `.bin/telemetrynames`, then runs it from the
repo root). Flags: `-root`, `-config`, `-all`.

Tests: `CGO_ENABLED=0 go test ./tools/telemetrynames/`. They build fixture code
and fixture specs in a temporary folder and drive `Check` and `run`.
