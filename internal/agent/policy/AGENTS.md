# internal/agent/policy

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- The file is untrusted text. Cap it (64 KiB), parse strictly, and never let any
  of its text into `PolicyInfo.error`, a log line, a metric label or an audit
  row. A `Problem` is built from a line number, a key name this package defines
  and a fixed phrase. A test with a secret-looking value guards this.
- Unknown key, duplicate key, wrong type, out-of-range value: safe mode. Never
  skip a key you do not understand. `exec.raw` is the one known key that is never
  honoured.
- Absent and `key:` mean no opinion (nil); `[]` means an opinion (none). Keep
  that difference in every new key.
- No polling and no timer that reads the file (ADR 0022 rule 6, ADR 0024
  decision 9). The only reads are: load, an event settle, `Recheck` (Snapshot
  build, Settings message), and doctor. A failed watch is logged and exposed; it
  never becomes a poll.
- The loaded policy is immutable. A change on disk only sets `changed_since_load`
  and fires `OnChange`.
- The agent never writes `policy.yaml` except `WriteStarter`, which never
  overwrites. `run` does not call it.
- Command kind names come from the `Command.kind` oneof descriptor, not a table.
- Locks: `checkMu` then `mu`; nothing slow (audit sync, callback, log) under `mu`.
- A change here updates this README, `doc.go`, 03e and defaults.md in the same
  commit.
