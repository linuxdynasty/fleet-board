# cmd/fleetd

`fleetd` is the hub: the one process that holds state.db and serves the dashboard and CLI.

```bash
fleetd init [--config PATH]   # write fleetd.yaml if missing, the state folder and state.db
fleetd run  [--config PATH]   # start the hub; SIGTERM or Ctrl-C stops it cleanly
fleetd enroll-token --machine NAME [--expect-host H] [--ttl 15m] [--label L ...] [--start-drained]
                              # ask the running hub for a token that enrolls one machine
fleetd --version              # "fleetd dev", or the release version set with -ldflags -X
```

`enroll-token` prints only the token on stdout, on one line, so
`TOKEN=$(fleetd enroll-token --machine laptop)` works; stderr shows the
expiry, the CA fingerprint and the `fleet-agent enroll --token …` line to run
on the machine. `--ttl` is more than 0 and at most 24h (default 15m). With no
hub running it prints "fleetd is not running", exits 1 and writes nothing.

The config file is `--config`, else `$FLEETD_CONFIG`, else
`~/.config/fleetd/fleetd.yaml`. Exit codes: 0 done (or stopped by a signal), 1
the command failed (the reason is on stderr and in the `hub.start_failed` log
line), 2 bad usage.

Today `run` serves the agent listener (TLS 1.3, every `AgentService` method
Unimplemented), `/metrics` on the loopback API listener, and the admin socket
`<state_dir>/admin.sock` (mode 0600, only fleetd's own OS user) that
`enroll-token` talks to. A second `run` against the same state folder is
refused while the first holds `<state_dir>/fleetd.lock`.

Build and try it:

```bash
make build
go run ./cmd/fleetd --version
```

`main.go` and `enroll.go` only parse flags and call `internal/hub/daemon`; what
fleetd does is specified in `docs/specs/02j-hub-runtime.md`, and the build order
is in `docs/plan/v1-implementation-plan.md` (plan rows 2 to 5). `main_test.go`
runs the built binary: init, run, a refused second run, SIGTERM, and a start
after a SIGKILL. `enroll_test.go` is row 2b's acceptance: init, run, and
`enroll-token` printing a token whose CA hash equals the stored CA; refused
lifetimes; "fleetd is not running" with no hub and after a SIGKILL; and
`admin.sock` removed by SIGTERM.
