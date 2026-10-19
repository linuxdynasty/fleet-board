# internal/fleetcli

`Run` is the testable entry point for the `fleet` command. It parses six read
commands, calls the generated FleetService client through the real local TCP
listener, and prints one complete result. List commands fetch one page; the
next page token is shown without changing it. The `doctor` command reads the
approval secret from a real terminal with echo off, then sends it only on
`GetStoreStatus`.

The current build accepts only loopback `http://` hubs. It refuses a present
`FLEET_TOKEN`, including an empty value. Token support belongs to a future
authenticated API release. Run focused checks with
`CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/fleetcli`.
