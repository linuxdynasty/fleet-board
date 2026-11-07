# internal/hub/logstore

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store`, `fleet-security` and `fleet-telemetry-coverage`.

Rules for this package:

- `logs.db` has its own writer through `store.Store` named `logs`, with the
  row-25 migrations in `migrate.go`. Never edit an applied migration; add
  the next version. `logs.db` is hub state the hub creates; `Open` never
  uses `MustExist`.
- The accept rules are 03d W9 exactly: `offset + raw_length <= end` is
  `DUPLICATE`, `offset - gap_before == end` stores (offset-minus-gap, never
  end-plus-gap: the sum wraps mod 2^64), anything else is `HOLE`,
  an unmapped pane is `UNKNOWN_STREAM`. Every chunk gets its verdict; the
  ack follows the commit, never precedes it.
- Retention re-checks open streams inside the delete transaction: a chunk
  that lands between candidate selection and delete reopens the stream,
  and the delete is skipped instead of removing live data.
- The store never reads `state.db`: the pane-to-session map is the injected
  `Resolver`. The gateway owns that map; tests inject a fake.
- Retention deletes the oldest ended sessions first, one transaction per
  session, never a session with an open stream. The live-byte measure is
  `(page_count - freelist_count) * page_size`: freed pages not yet vacuumed
  do not count. Output is never dropped for the cap: over the cap with no
  finished session left, keep committing and report `no_final_session`.
- Search scans decompressed frames, strips ANSI, matches case-insensitively
  and bounds one call at 256 MiB of raw text with an opaque resume token.
  Snippets are at most 512 bytes; pane text is untrusted and may hold
  secrets, so it never reaches a log line or metric label.
- Tests use real files in `t.TempDir()` and zstd frames the tests encode
  themselves. The replay test works its expectations out by hand from the
  raw lengths, never by recomputing what the code does. Prune tests use
  incompressible noise so deletes move whole pages like at production
  scale. Run with `-race`.
- A change here updates this file, `README.md` and `doc.go` together.
