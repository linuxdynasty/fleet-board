# internal/hub/discovery

The hub's mDNS advertisement: it makes `fleetd` findable as `_fleetd._tcp` on
the local network, so an agent that has no cached or configured address can
find it (plan row 5, [02j](../../../docs/specs/02j-hub-runtime.md#hub-runtime),
"Discovery"; ADR 0028; the agent's side is `internal/agent/conn`).

```go
adv, err := discovery.New(discovery.Options{
    Enabled: cfg.Discovery.MDNS, Hostname: host, Listener: agentListener.Addr(),
    Cert: certs, Telemetry: tel,
})
adv.Start() // after the agent listener serves; never fails the hub
defer adv.Stop()
```

- **What is advertised.** Instance `fleetd-<host>`, service `_fleetd._tcp`,
  domain `local.`, SRV target `fleetd-<host>.local.` and the agent listener's
  port. `<host>` is the lower-cased, `.local`-stripped host name that
  `ca.DefaultNames` uses (`ca.ServiceTarget`), so the OS's own name, which the
  system responder owns, is never answered for. TXT is `proto=<min>-<max>`
  and nothing else: no fleet id, version or instance id.
- **Why the target is verified.** The agent verifies the hub's certificate
  against the SRV target, so the server certificate always holds it, even when
  `tls.agent_names` is set (`ca.AgentNames`; the daemon adds it for `init` and
  `run`). If the certificate served now does not name it (a re-issue failed),
  the advertiser refuses with `cert_missing_target`: in `fleetd run` a failed
  re-issue already stops the start, so this is the second line of defence.
- **Addresses: one interface, one address per family.** The agent's
  library client keeps only the last A record and the last AAAA record it
  reads, and the agent dials those, so every extra address is a gamble on
  which one is dialed (a Docker or VM bridge would be dialed by a LAN agent
  that can never reach it). The hub therefore advertises the addresses of
  **one** interface, and joins the mDNS group on that interface (ADR 0028
  decision 1: it works "with the interface set"). A listener bound to one
  address uses that address's interface. A listener on the wildcard address
  uses the interface that holds the default route's source address (found by
  connecting an unsent UDP socket, `RouteSource`), if it is a real one; else
  the first up, multicast interface that is not loopback, point-to-point or
  virtual (names `bridge*`, `utun*`, `awdl*`, `llw*`, `docker*`, `veth*`,
  `vmnet*`, `tun*`, `tap*`, `vboxnet*`, `tailscale*`, `br-*`, `virbr*`,
  `wg*`) and has an address. On it, the route's source address (else the first
  one the system lists) is the IPv4 record and likewise one IPv6 record.
  Loopback, unspecified, multicast, broadcast and link-local addresses (IPv6
  has no zone in an AAAA record; 169.254/16 is no network) are never
  advertised. A host with none left advertises nothing and logs
  `discovery.advertise_refused{reason=no_addresses}`. The choice is made once,
  at start. Re-advertising on a network change is later work: until then, after
  a network change restart `fleetd` or give the agent the new address (the
  agents' cached and configured addresses and the `HelloAck` list cover the
  time in between).
- **Never fatal.** A refusal or failure is one log line and one count; the hub
  keeps serving. `NewServer` of the library hides a family that cannot bind,
  so the advertiser checks `udp4` and `udp6` itself, logs each failed family
  once (`discovery.family_unavailable`) and advertises on the other; it fails
  (`advertise_failed{step=bind}`) only when neither binds.
- **No multicast in tests.** `Env.Responder` and `Env.Addrs` are the seams;
  the zero value is the real machine. The live check is behind
  `FLEET_MDNS_LIVE=1` in `internal/agent/conn` (it needs the agent's browser).
- Signals: see [05b](../../../docs/specs/05b-observability.md).

Tests: `CGO_ENABLED=0 go test ./internal/hub/discovery/`.
