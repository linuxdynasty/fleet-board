# internal/hub/discovery

The hub's mDNS advertisement: it makes `fleetd` findable as `_fleetd._tcp` on
the local network, so an agent that has no cached or configured address can
find it (plan row 5, [02j](../../../docs/specs/02j-hub-runtime.md#hub-runtime),
"Discovery"; ADR 0028; the agent's side is `internal/agent/conn`).

```go
adv := discovery.New(discovery.Options{
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
  address uses the interface that holds it, however virtual: the owner chose
  it, but it must be up and able to multicast. A listener on the wildcard
  address uses, in this order (in a first round without IPv4 link-local
  addresses, then a second with them):
  1. the interface holding the default route's source address (found by
     connecting an unsent UDP socket, `RouteSource`), when it is up,
     multicast, not point-to-point and not named like a tunnel (`tun*`,
     `tap*`, `wg*`, `utun*`, `tailscale*`, `zt*`, `nordlynx*`, `ppp*`). Its
     name may look like a bridge: a Linux host that bridges its NIC holds its
     address and the route on `bridge0`, and a management NIC beside it is
     not the way in;
  2. else the first reachable interface: up, multicast, not loopback, not
     point-to-point and not named like a bridge, container, VM or tunnel
     (`bridge*`, `utun*`, `awdl*`, `llw*`, `docker*`, `veth*`, `vmnet*`,
     `tun*`, `tap*`, `vboxnet*`, `tailscale*`, `br-*`, `virbr*`, `wg*`,
     `podman*`, `cni*`, `lxcbr*`, `lxdbr*`, `incusbr*`, `zt*`, `flannel*`,
     `cali*`, `cilium*`, `nordlynx*`, `ppp*`). With no default route this is only a
     guess;
  3. else the route holder even when it is tunnel-named, if it is up,
     multicast and not point-to-point (a `tap` device that is the only
     addressed route holder is advertised).

  A point-to-point interface is never used. On the chosen interface the
  route's source address (else the first one the system lists) is the IPv4
  record, and likewise one IPv6 record. That IPv6 address can be a temporary
  privacy address that expires; the list is read once, so an IPv6-only hub
  needs a restart then (agents try IPv4 first).
  Loopback, unspecified, multicast, broadcast and IPv6 link-local addresses
  (an AAAA record has no zone) are never advertised. An IPv4 link-local
  address (169.254/16, a direct cable with no DHCP) ranks last: it is
  advertised only when no interface has anything else. A host with nothing
  left advertises nothing and logs
  `discovery.advertise_refused{reason=no_addresses}`. The choice is made once,
  at start. Re-advertising on a network change is later work: until then, after
  a network change restart `fleetd` or give the agent the new address (the
  agents' cached and configured addresses and the `HelloAck` list cover the
  time in between).
- **Instance names.** The instance is `fleetd-<first label of the host>`;
  hubs `hub.a.example` and `hub.b.example` on one network would both be
  `fleetd-hub`. The SRV target keeps the whole host and stays unique, and the
  agent verifies the target, so a clash costs a duplicate service name, not a
  wrong connection.
- **Never fatal.** A refusal or failure is one log line and one count; the hub
  keeps serving. `NewServer` of the library hides a family that cannot bind,
  so the advertiser checks `udp4` and `udp6` itself, logs each failed family
  once (`discovery.family_unavailable`) and advertises on the other; it fails
  (`advertise_failed{step=bind}`) only when neither binds.
- **No multicast in tests.** `Env.Responder`, `Env.Ifaces` and `Env.Route` are the seams;
  the zero value is the real machine. The live check is behind
  `FLEET_MDNS_LIVE=1` in `internal/agent/conn` (it needs the agent's browser).
- **A branch that cannot be driven from a real meter:** if the instruments
  cannot be registered, `New` logs `discovery.advertise_failed{step=telemetry}`
  once and the hub advertises with no counters (tested with a meter that
  refuses; the OpenTelemetry SDK itself does not fail on these fixed names).
- Signals: see [05b](../../../docs/specs/05b-observability.md).

Tests: `CGO_ENABLED=0 go test ./internal/hub/discovery/`.
