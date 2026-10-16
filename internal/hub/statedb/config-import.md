# Config import and pending machines

(migrations 16, 20, plan row 13a; 02m, 02b "Idempotency")

`import_request_log` keys (`principal_id`, `request_id`) to the body hash
and stored response (24 h replay, bounded prune). `ApplyConfigImport`
applies a verified plan in one writer transaction or nothing, with one
`config.import` audit row. A machine op carries the baseline version: a
machine moved since the dry run is `ErrImportStale` (heartbeats never bump
it). `pending_machines` keeps one row per unknown name; enroll consumes
the match (merging labels, draining, storing the layer), a rename onto a
kept name leaves the shadow the next import heals.
