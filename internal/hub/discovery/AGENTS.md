# internal/hub/discovery

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- Advertising is best effort and never fatal (ADR 0028, 02j): a refusal or a
  failure is logged and counted once and the hub keeps serving. Never return
  an error from `Start` or fail the daemon over it.
- The SRV target comes from `ca.ServiceTarget` and only that; never advertise
  the OS host name, a loopback, unspecified, multicast or IPv6 link-local
  address, a second interface (the agent keeps one address per family), or a
  name the served certificate lacks (`cert_missing_target`).
- TXT is `proto=<min>-<max>` only. Never put the fleet id, the hub version,
  the instance id or any other identifier on the network.
- The only code that opens a multicast socket is `responder.go`. Tests inject
  `Env.Responder`, `Env.Ifaces` and `Env.Route`; the default `go test` sends
  no multicast and joins no group. Real sockets only behind `FLEET_MDNS_LIVE=1`.
- Logs and labels are counts and closed reasons (`Reason`, `step`,
  `err_kind`); never the names, the addresses or error text, which can echo
  the network.
- `Start` and `Stop` run on the one goroutine that owns the listeners; the
  package starts none of its own.
- A change here updates this README, `doc.go`, 02j and 05b in the same commit.
