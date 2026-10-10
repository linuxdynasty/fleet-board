# cmd/fleet-agent

`fleet-agent` is the agent: one per machine, it watches tmux sessions and runs commands for the hub.

Today it can join a machine to the fleet:

```bash
TOKEN=$(fleetd enroll-token --machine laptop)      # on the hub
fleet-agent enroll --hub hub.local --token "$TOKEN" # on the machine; port 7443 if none
fleet-agent enroll --token "$TOKEN"                 # or find the hub on the local network
```

`enroll` checks the hub against the CA named in the token before it sends
anything, then saves the machine's identity in the state folder
(`~/.local/state/fleet-agent/`, or `--state-dir`), mode 0700 with 0600 files.
It prints one line on stdout naming the machine and the fleet; log lines go
to stderr. Exit codes: 0 enrolled, 1 the enroll failed (the line on stderr
says why: no hub found, a bad or expired or used token, a hub that does not
match the token, a hub that stayed busy, or a machine that is already
enrolled), 2 bad usage. A stray argument (say a token typed without
`--token`) is bad usage, and the error never repeats it.

- Without `--hub` it asks the local network (mDNS) for the hub and sends
  the token only to one that proves it holds the token's CA. If none does,
  it exits 1 having sent nothing and says to pass `--hub`: a firewall on
  the machine (ufw on Linux, say) often hides the hub from mDNS.
- A busy hub or a dropped connection is retried a few times with the same
  token and key (docs/specs/defaults.md, "Enroll tries").
- If the reply is still lost, run the same command again: the hub returns
  the same machine and certificate.
- If the hub expected another hostname (the one the token named, or else
  the machine name), enroll still works and one warning line on stderr
  names both hostnames.
- If the hub failed on its own side, the error does not suggest running
  the command again: it would fail the same way; fleetd's log says why.
- Until re-enroll exists (later in plan row 4), an enrolled machine is never
  re-enrolled over its identity. To start again, remove only
  `identity.json` (and `enroll.key` if present) from the state folder and
  keep `agent.db`: it holds the install id, which stays with the machine.
  The next enroll removes `cert.pem` and `renew.key`, the old identity's
  renewed certificate, before it saves the new one.

Once enrolled, `fleet-agent run` keeps the machine connected to its hub:

```bash
fleet-agent run    # logs to stderr; stop with Ctrl-C or SIGTERM
```

- It takes `fleet-agent.lock` in the state folder first. A second `run` on
  the same folder exits 1 and names the lock; the first keeps running.
- It tries the hub address it last reached, then the hub found by one mDNS
  browse, then the hub's latest list of addresses (`hub-addresses`, saved
  when the hub sends a new one), else the list it enrolled with, over mTLS.
  An address from mDNS is only a candidate: it must pass the same check. When the hub is down it waits and tries again (1 s growing to
  60 s, with some randomness); waking from sleep or a network change makes
  it try at once.
- While connected it renews its certificate at two thirds of its life
  (checked on each connect and hourly): a new key, the new pair saved in
  `cert.pem`, used from the next connection; `identity.json` is never
  changed. A failed renewal is tried again at the next check and never
  stops `run`.
- Exit codes: 0 after a signal (it says goodbye to the hub first; a
  signal while it is still starting is a clean stop too, logged as
  `run.stopped{step}`; a real startup failure at the same moment still
  exits 1 with `run.failed`), 1 when
  the machine is not enrolled, another `run` holds the folder, or the loop
  ended on its own: the machine was revoked or is unknown to the hub, the
  hub belongs to another fleet, or the certificate expired. The last log
  line says which.
- When the hub revoked the machine, did not know it, or was another fleet,
  `run` saves that in `ended.json` before it exits. The next `run` (say a
  supervisor restarting it) reads the file before it touches the network,
  logs `run.refused`, prints the fix and exits 1. For a revoked or unknown
  machine: remove `identity.json` (keep `agent.db`), then
  `fleet-agent enroll --token <new>`; enroll removes `ended.json`. For
  another fleet: check the hub address, then re-enroll or remove
  `ended.json` to try again. A file left from an earlier machine id is
  removed. A damaged `ended.json` also exits 1 before any network call
  (`run.failed{step="ended", err_kind="parse"}`), saying to remove it.
- `run` keeps `status.json` in the state folder (mode 0600): the connection
  state, the last failure per hub address, the last mDNS browse, whether the
  push socket is off, the clock offset, and the end it stopped on. It is
  written at start, at least once a minute, and once more when `run` stops,
  so it shows the stop. A value not yet known is left out. `fleet-agent
  doctor` (plan row 6) reads it and prints its age, so it works while the
  agent is down. Until plan row 6 PR 6a lands, a change shows in the file
  within a minute.
- A failed `enroll` leaves its failure code in `enroll.json` (a closed word
  such as `token_malformed`, `hub_untrusted` or `refused` with the hub's
  reason name; never the token, the address or the message), only into a
  state folder that exists. A good `enroll` removes the file, a refusal
  because the machine is already enrolled leaves it alone, and `run`
  removes it once it has an identity.

`fleet-agent --version` prints the name and the version; `-h` prints the
usage. Release builds set the version with `-ldflags -X` (see
`internal/buildinfo`); other builds say `dev`.

```bash
make build
go run ./cmd/fleet-agent --version
```

The tests build the binary with `-tags fleet_nomdns`, whose hub finder
finds nothing, so they never send an mDNS query; `FLEET_MDNS_LIVE=1`
builds the real one. In-process tests pass their own finder to `execute`
and `runAgent`.

The logic is in `internal/agent/conn`. What the agent will do is specified
in `docs/specs/01-agent-capabilities.md`; the build order is in
`docs/plan/v1-implementation-plan.md` (plan row 4).
