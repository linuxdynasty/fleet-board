# cmd/fleet-agent

`fleet-agent` is the agent: one per machine, it watches tmux sessions and runs commands for the hub.

Right now it is a scaffold: `fleet-agent` and `fleet-agent --version` print the name and the
version, `-h` prints the usage. Release builds set the version with
`-ldflags -X` (see `internal/buildinfo`); other builds say `dev`.

Build and run it:

```bash
make build
go run ./cmd/fleet-agent --version
```

What it will do is specified in `docs/specs/01-agent-capabilities.md`; the build order is in
`docs/plan/v1-implementation-plan.md` (plan row 4).
