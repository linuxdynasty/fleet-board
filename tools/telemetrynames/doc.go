// Command telemetrynames checks that every telemetry name in fleet-board's Go
// code is documented in the right spec (make telemetry-names).
//
// It parses the non-test .go files under the config's code_dirs (skipping
// gen/, testdata/ and vendor/) with go/parser and reads two kinds of name:
//
//   - log events: the message argument of a slog call, the first argument of
//     Debug, Info, Warn and Error, the second of the *Context variants and
//     the third of Log and LogAttrs. A call on an imported package other than
//     log/slog (http.Error) is not a log call.
//   - metrics: the first argument of a metric.Meter instrument constructor
//     (Int64Counter, Float64Histogram, Int64ObservableGauge, ...), either a
//     literal or `prefix + "_tail"`, of which the static tail is kept.
//
// A name that is not a static string fails. Each name's log area ("hub.") or
// metric tail prefix ("writer_") maps, in .agents/telemetry-docs.json, to the
// one spec that must hold it as a whole token; an unmapped area fails, and so
// does a name that only another spec lists. Spec names with no code are
// printed as a report. [Check] is the seam the tests drive.
//
// Exit codes: 0 clean, 1 findings, 2 a usage or input error.
package main
