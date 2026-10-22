# internal/fleetcli

`Run` is the testable entry point for the `fleet` command. It parses the read
commands (`status`, `machines`, `accounts`, `goals`, `work`, `doctor`,
`export`), the owner-configuration writes (`repo`, `account`, `settings`,
`machine`, `import`), calls the generated FleetService client through the
real local TCP listener, and prints one complete result. List commands fetch
one page; the next page token is shown without changing it. The writes and
`doctor` read the approval secret from a real terminal with echo off, then
send it only in the secret header on their protected RPCs; destructive writes
need `--yes`. `export` prints `fleet.yaml` (the existing `fleetconfig`
codec renders it; `--json` keeps the raw response), and `import` shows the
server plan before applying it with its `plan_hash`.

The current build accepts only loopback `http://` hubs. It refuses a present
`FLEET_TOKEN`, including an empty value. Token support belongs to a future
authenticated API release. Run focused checks with
`CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/fleetcli`.
