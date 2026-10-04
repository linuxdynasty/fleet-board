# cmd/fleetd

`fleetd` is the hub: the one process that holds state.db and serves the dashboard and CLI.

Right now it is a scaffold: `fleetd` and `fleetd --version` print the name and the
version, `-h` prints the usage. Release builds set the version with
`-ldflags -X` (see `internal/buildinfo`); other builds say `dev`.

Build and run it:

```bash
make build
go run ./cmd/fleetd --version
```

What it will do is specified in `docs/specs/02j-hub-runtime.md`; the build order is in
`docs/plan/v1-implementation-plan.md` (plan rows 2 to 5).
