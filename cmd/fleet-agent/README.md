# cmd/fleet-agent

`fleet-agent` is the agent: one per machine, it watches tmux sessions and runs commands for the hub.

Today it can join a machine to the fleet:

```bash
TOKEN=$(fleetd enroll-token --machine laptop)      # on the hub
fleet-agent enroll --hub hub.local --token "$TOKEN" # on the machine; port 7443 if none
```

`enroll` checks the hub against the CA named in the token before it sends
anything, then saves the machine's identity in the state folder
(`~/.local/state/fleet-agent/`, or `--state-dir`), mode 0700 with 0600 files.
It prints one line on stdout naming the machine and the fleet; log lines go
to stderr. Exit codes: 0 enrolled, 1 the enroll failed (the line on stderr
says why: no `--hub`, a bad or expired or used token, a hub that does not
match the token, a hub that stayed busy, or a machine that is already
enrolled), 2 bad usage. A stray argument (say a token typed without
`--token`) is bad usage, and the error never repeats it.

- `--hub` is required for now: finding the hub on the network comes later
  in plan row 4. Without it nothing is sent.
- A busy hub or a dropped connection is retried a few times with the same
  token and key (docs/specs/defaults.md, "Enroll tries").
- If the reply is still lost, run the same command again: the hub returns
  the same machine and certificate.
- If the token named another hostname, enroll still works and one warning
  line on stderr names both hostnames.
- Until re-enroll exists (later in plan row 4), an enrolled machine is never
  re-enrolled over its identity. To start again, remove only
  `identity.json` (and `enroll.key` if present) from the state folder and
  keep `agent.db`: it holds the install id, which stays with the machine.

`fleet-agent --version` prints the name and the version; `-h` prints the
usage. Release builds set the version with `-ldflags -X` (see
`internal/buildinfo`); other builds say `dev`.

```bash
make build
go run ./cmd/fleet-agent --version
```

The logic is in `internal/agent/conn`. What the agent will do is specified
in `docs/specs/01-agent-capabilities.md`; the build order is in
`docs/plan/v1-implementation-plan.md` (plan row 4).
