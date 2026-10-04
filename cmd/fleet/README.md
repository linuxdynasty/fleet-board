# cmd/fleet

`fleet` is the CLI: a client of the hub API for the person at the keyboard.

Right now it is a scaffold: `fleet` and `fleet --version` print the name and the
version, `-h` prints the usage. Release builds set the version with
`-ldflags -X` (see `internal/buildinfo`); other builds say `dev`.

Build and run it:

```bash
make build
go run ./cmd/fleet --version
```

What it will do is specified in `docs/specs/02k-v1-surface-and-cli.md`; the build order is in
`docs/plan/v1-implementation-plan.md` (plan row 15).
