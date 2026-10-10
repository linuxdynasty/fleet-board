# internal/agent/audit

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- Closed fields only. Never add a free-text field, a message, a path, a screen
  or a file's text to `Entry`; a new detail key is a short fixed name, and its
  value a code, a hash or an enum name. The `closed` pattern is the gate; do not
  loosen it to make a caller pass.
- A failed audit write never blocks or fails the product. `Append` has no
  return value on purpose. Count and log it; do not return the error.
- The file is opened `O_APPEND|O_CREATE`, mode 0600, `O_NOFOLLOW`, synced per
  line. Do not rewrite the whole file (that is what `writePrivate` in
  `internal/agent/conn` does and it does not fit an append log).
- New actions are the 02g audit table's names; add the name here and in 02g's
  table in the same commit.
- Never log an entry's fields or the error text; `audit.write_failed` carries
  `err_kind` and a count.
- Doctor never parses `audit.log` (plan row 6).
- A change here updates this README, `doc.go`, 05b (the log line and the counter)
  and defaults.md ("Agent local audit log") in the same commit.
