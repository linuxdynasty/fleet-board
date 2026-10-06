# internal/hub/daemon

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security`, `fleet-sqlite-store` and `fleet-telemetry-coverage`. Rules
for this package:

- Order is the contract: config, folder check, `fleetd.lock`, then
  `state.db`. Never open the store before the lock is held.
- `Run` never creates `state.db` and never mints an identity; `Init` never
  replaces a store.
- Every start refusal goes through `session.fail` with a closed `Reason`: one
  `hub.start_failed` line and one count. A new reason updates the README and
  05b. Never put the error text, a path or key material in a log field.
- Every goroutine `Run` starts is waited for before the store closes.
- Tests run the real `Init` and `Run` against real files and sockets; keep
  state folders short (under `/tmp`) because config checks the socket path.
- A change here updates this README and `doc.go` in the same commit.
