# cmd/fleetd

`fleetd` is the hub: the one process that holds state.db and serves the dashboard and CLI.

```bash
fleetd init [--config PATH]   # write fleetd.yaml if missing, the state folder and state.db
fleetd run  [--config PATH]   # start the hub; SIGTERM or Ctrl-C stops it cleanly
fleetd --version              # "fleetd dev", or the release version set with -ldflags -X
```

The config file is `--config`, else `$FLEETD_CONFIG`, else
`~/.config/fleetd/fleetd.yaml`. Exit codes: 0 done (or stopped by a signal), 1
the command failed (the reason is on stderr and in the `hub.start_failed` log
line), 2 bad usage.

Today `run` serves the agent listener (TLS 1.3, every `AgentService` method
Unimplemented) and `/metrics` on the loopback API listener. A second `run`
against the same state folder is refused while the first holds
`<state_dir>/fleetd.lock`. The admin socket and `fleetd enroll-token` arrive
with the second PR of plan row 2b.

Build and try it:

```bash
make build
go run ./cmd/fleetd --version
```

`main.go` only parses flags and calls `internal/hub/daemon`; what fleetd does is
specified in `docs/specs/02j-hub-runtime.md`, and the build order is in
`docs/plan/v1-implementation-plan.md` (plan rows 2 to 5). `main_test.go` runs the
built binary: init, run, a refused second run, SIGTERM, and a start after a
SIGKILL.
