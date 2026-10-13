# internal/hub/secret

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- The secret file holds 32 raw bytes and nothing else, mode 0600, in the
  state folder beside `state.db`. `Ensure` creates it once and never
  replaces it; an existing file is only mode-checked.
- The secret value never reaches a log, an error, a metric label or an
  audit row. Errors say only that it is missing or wrong (`ErrMissing`,
  `ErrRefused`).
- Compare with `subtle.ConstantTimeCompare` only; never `==`.
- Tests use `t.TempDir()` and `crypto/rand`; never a fixed secret in a
  committed file.
