# cmd/fleet

`fleet` is the CLI: a client of the hub API for the person at the keyboard.

`fleet` reads the local hub through its public TCP API. It serves `status`,
`machines`, `accounts`, `goals`, `work`, and `doctor`. The four list commands
also serve `show` for one resource. `--json` prints the complete Connect
response. `-h` prints command help; `--version` reports the linked version.
Release builds set that version with `-ldflags -X` (see `internal/buildinfo`);
other builds say `dev`.

Build and run it:

```bash
make build
go run ./cmd/fleet --version
```

Set `FLEET_URL` to a local loopback `http://` address, or use the default
`http://127.0.0.1:7080`. This build refuses `FLEET_TOKEN`. `doctor` asks for the
local approval secret on a terminal; its prompt goes to stderr even with
`--json`. The remaining commands and future writes are tracked by plan row 15
in `docs/plan/v1-implementation-plan.md`.
