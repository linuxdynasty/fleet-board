# 0023. Additional libraries: mDNS and system sampling

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Builds on: [0020](0020-dependencies.md) (library table and the rule that a
  new runtime dependency needs a line or an ADR),
  [0004](0004-agent-connectivity.md) (mDNS discovery),
  [0001](0001-system-architecture.md) (no cgo)
- Supersedes in part: [0022](0022-drift-audit-decisions.md) decision 3 (the
  mDNS and system sampling rows; the ULID row stands)

## Context

The v1 plan needs two things ADR 0020 does not list: finding and advertising
`_fleetd._tcp` over mDNS (capability A2, plan PR 4) and sampling load, memory
and free disk space on macOS and Linux (B2, plan PR 6). ADR 0022 first
answered "no library: run `dns-sd` or `avahi` and read `/proc`". Candidate
libraries were then built and run with `CGO_ENABLED=0`, Go 1.27.1, for darwin
and linux on amd64 and arm64, and on an arm64 Mac they returned real cpu,
memory, load, disk and boot-time values. That showed a library is practical, so
this ADR chooses one in place of the tool exec and the hand-written sampler.

## Options

**mDNS and DNS-SD** (all MIT, none needs cgo):

| Library | Version | Last release | Status |
|---|---|---|---|
| `hashicorp/mdns` | v1.0.7 | 2026-06-11 | maintained; deps are `miekg/dns` and `x/net` only |
| `brutella/dnssd` | v1.2.14 | 2024-10-22 | maintained, one maintainer, slow releases; pulls `vishvananda/netlink` |
| `grandcat/zeroconf` | v1.0.0 | 2020-01-29 | dormant; 2021-era dependencies |
| `libp2p/zeroconf/v2` | v2.2.0 | 2022-08-07 | quiet fork of grandcat |
| `pion/mdns/v2` | v2.2.2 | 2026-10-01 | active; lower level (conn-based responder and resolver), not evaluated in depth |
| exec `dns-sd` / `avahi-*` | n/a | n/a | ADR 0022's choice; needs a tool on Linux and a parser for its output |

`hashicorp/mdns` browses with `QueryContext(ctx, &QueryParam{Service:
"_fleetd._tcp", ...})` and advertises with `NewMDNSService` plus `NewServer`.
Browsing is query-and-timeout polling: there is no continuous watch and no
goodbye event, so the agent re-queries on a loop. Per-interface control is
minimal. `brutella/dnssd` has a real responder (probing, conflict handling) and
a continuous `LookupType` browse with add and remove callbacks.
`grandcat/zeroconf` has the cleanest API, but a dormant project is not a safe
base.

**System sampling:**

| Option | Notes |
|---|---|
| `github.com/shirou/gopsutil/v4` v4.26.9 (2026-09-27), BSD-3-Clause | very active, about 11.9k stars. Linux reads `/proc` in pure Go. Darwin `cpu`, `mem`, `load`, `disk` and `host` need no cgo: they use `x/sys/unix`, `go-sysconf` and `ebitengine/purego`, which loads system libraries without cgo. Cross-compiled for all four targets. `go.mod` says `go 1.26.0`; the module graph is large (Windows-only modules), so import only those five subpackages |
| `github.com/mackerelio/go-osstat` v0.2.8 (2026-07-08), Apache-2.0 | maintained, no cgo; narrower: no disk free, no boot time |
| `golang.org/x/sys` and `/proc` only (ADR 0022) | possible: `unix.Statfs`, sysctl, `/proc/loadavg` and `/proc/meminfo`. On darwin, CPU and exact memory need Mach `host_statistics`, which means cgo or purego |

## Decision

**mDNS: `github.com/hashicorp/mdns` v1.0.7**, MIT. Actively released, minimal
dependencies, covers both browse (agent) and advertise (hub). It sits behind a
small `Discover` interface in `internal/agent/conn` and a small `Advertise` in
`internal/hub/gateway`, so a replacement touches one file each. The agent
re-queries `_fleetd._tcp` on a loop while it has no connection (the interval
and browse timeout are set in PR 4 and written to
[defaults](../specs/defaults.md)). Because there is no
goodbye event, a discovered address is only a candidate: it is accepted when TLS
verifies against the pinned CA, as before (02g).

**Fallbacks, in order.** `github.com/brutella/dnssd` if the agent needs a
continuous browse, goodbye events or conflict handling. Exec of `dns-sd` (macOS)
or `avahi-publish-service` (Linux), as ADR 0022 first chose, if PR 4's check
shows the hub's advertisement conflicts with macOS's own mDNS responder on
port 5353 (the research did not exercise advertising next to the system
responder).

**Sampling: `github.com/shirou/gopsutil/v4` v4.26.9**, BSD-3-Clause, importing
only `cpu`, `mem`, `load`, `disk` and `host`. It feeds `ResourceSample` (B2):
`load.Avg`, `mem.VirtualMemory`, `disk.Usage` per repo root and the state
directory. `cpu.Percent` needs two samples, so the agent keeps its own previous
sample. The macOS memory pressure level
(`kern.memorystatus_vm_pressure_level`) is one `unix.SysctlUint32` call through
`golang.org/x/sys`, which gopsutil already requires; on Linux the level comes
from `/proc/pressure/memory`, with `MemAvailable` as the fallback, as in ADR
0022. Fallback if gopsutil's graph or a release becomes a problem: a thin
in-house sampler on `x/sys`, giving up exact CPU and memory on darwin unless it
adds purego.

The ULID row of ADR 0022 stands: `github.com/oklog/ulid/v2` (Apache-2.0),
lowercased.

Versions are pinned and licenses re-checked in the PR that first imports each
(ADR 0020, rules for changing dependencies); `go.mod` must still build with
`CGO_ENABLED=0` for darwin and linux on amd64 and arm64.

## Consequences

- Linux no longer needs `avahi` for discovery. `fleet-agent doctor` reports a
  host with no usable multicast interface instead of a missing tool, and
  `discovery.mdns: false` still turns advertising off.
- ADR 0020's table gains two lines (the table is not edited; read it together
  with this ADR). Two more dependencies, one with a large module graph, enter
  the supply chain; each upgrade reads the release notes.
- The re-query loop is the agent's job; the connection-attempt metric keeps its
  `source=mdns` label (03).
- Plan PR 4 checks advertising next to the macOS responder before the exec
  fallback is ruled out, and PR 6 checks the sampler on the four machines.

## Open questions

1. Does `hashicorp/mdns` advertise reliably on a macOS hub that also runs the
   system mDNS responder? Decided by a check in plan PR 4.
2. The re-query interval and the browse timeout are chosen with PR 4's
   measurements.
