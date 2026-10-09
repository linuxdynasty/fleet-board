# internal/agent/wakenet

Tells `fleet-agent`'s connection loop that the machine just woke up or that its
network really changed, so it reconnects at once instead of waiting about 30 s
for transport keepalive to notice a dead connection. Spec 01 A6, 03 A2,
[ADR 0029](../../../docs/adr/0029-wake-and-network-change-detection.md); numbers
in [defaults](../../../docs/specs/defaults.md). Evidence:
[spike 0.6](../../../spikes/wakenet/results/README.md).

## Use

```go
out := make(chan wakenet.Event)
wake, netw := wakenet.NewWake(log), wakenet.NewNet(log)
go wake.Run(ctx, out)
go netw.Run(ctx, out)
for ev := range out {
	d := wakenet.Decide(ev, wakenet.Conn{Connected: up, LocalAddr: local})
	wakenet.LogDecision(ctx, log, ev, d)
	// d.Reconnect: reset the backoff, close the stream if d.CloseStream,
	// wait the short random delay, connect with Hello.reason = d.Reason.
}
```

`Net.PushOff()` is true once the push socket could not be opened or died;
`fleet-agent doctor` (plan row 6) reports it.

## Wake

One check every 5 s, two signals; either growing by 2 s or more since the last
check is one wake, and both in one check are still one:

- **Time asleep since boot.** Linux `CLOCK_BOOTTIME` minus `CLOCK_MONOTONIC`,
  macOS `CLOCK_MONOTONIC_RAW` minus `CLOCK_UPTIME_RAW`, through
  `unix.ClockGettime`. The clock that stops during sleep is read before and
  after the other one and the midpoint is used, and the read is tried again if
  those two reads are more than 10 ms apart, so a thread switch between reads
  cannot fake a sleep. Reading two clocks in turn can still give a tiny
  negative number (‑209 ns was seen on the Mac mini); anything above ‑1 s
  counts as zero. A failed read, or one at ‑1 s or below, logs
  `wake.clock_read_failed`, is never a wake, and keeps the last good reading,
  so a sleep across a failed check is still counted on the next one.
- **Wall clock past Go's monotonic clock.** Go's monotonic clock stops during
  sleep on both systems; the wall clock does not. A wall clock stepped back
  2 s or more logs `wake.wall_clock_back` and is never a wake.

## Network change

The push socket (macOS `PF_ROUTE`, Linux `NETLINK_ROUTE` with the link, address
and route groups; no root) is only a hint. A hint is a link message, an address
added or removed, or a default route added, removed or changed; route lookups
(`RTM_MISS`, `RTM_GET`) are not. A read the parser cannot make sense of counts
as a hint, and so does Linux `ENOBUFS` (the kernel dropped messages): a hint
only starts a read.

After a hint the watcher waits until 500 ms pass with no further hint, never
more than 2 s after the first, then reads the interfaces that are up and
running, their addresses and the default route of each family, and compares
with the last reading. No difference, no event. The omarchy IPv6 refresh (the
router re-announces the same address every few seconds to two minutes) is a
replay test: every refresh reads, finds nothing, and sends nothing.

If the push socket cannot be opened or stops working, the watcher logs
`netchange.push_off` once and reads every 15 s instead. There is no poll while
the push socket works.

### Default route: how it is read

- **macOS: the routing table dump**, `sysctl` `CTL_NET, PF_ROUTE, 0, AF_UNSPEC,
  NET_RT_DUMP, 0` through `unix.SysctlRaw` (the same data `netstat -rn`
  reads). The default route of a family is the first one that is up, has an
  all-zero destination and netmask, is not a host route, and is not scoped to
  one interface (`RTF_IFSCOPE`, the `I` flag: macOS keeps one per utun and
  bridge, and uses them only for sockets bound to that interface).
  Chosen over keeping a default route built from push messages because the
  watcher needs a full reading at start and on every 15 s poll, when there is
  no push message to build from, and because a dropped push message would
  leave a built route wrong until the next one. The dump costs one system call
  and only runs after a real hint. Checked on the Mac mini on 2026-10-09: the
  parsed route (`en0` via `192.168.1.1`, no unscoped IPv6 default) matched
  `route -n get default` and `route -n get -inet6 default`.
- **Linux: a netlink route dump**, `RTM_GETROUTE` through the standard
  library's `syscall.NetlinkRIB`. The default route of a family is the
  main-table unicast route with a zero-length destination and the lowest
  metric; a multipath route takes its first next hop. Checked in a Linux
  container: the parsed route matched `ip route`.

Both parsers are pure Go with no build tag, tested with fixtures built byte by
byte from the kernel headers, and fuzzed. Interfaces and addresses come from
`net.Interfaces`.

## Policy (`Decide`)

| Event | Not connected | Connected |
|---|---|---|
| Wake | reset backoff, attempt (`CONNECT_REASON_WAKE`) | close the stream, reconnect (`CONNECT_REASON_WAKE`) |
| Network change, default route changed | reset backoff, attempt | close the stream, reconnect |
| Network change, the connection's local address was on an up interface and now is on none | reset backoff, attempt | close the stream, reconnect |
| Any other network change | reset backoff, attempt | nothing |

Network-change reconnects carry `CONNECT_REASON_NETWORK_CHANGE`. The local
address counts as lost only when the last reading held it and this one does
not, so a change unrelated to the connection never reconnects. The 5 s minimum
between connects and the 20 to 120 ms random delay are the connection loop's.

## Logs

`wake.detected`, `wake.wall_clock_back`, `wake.clock_read_failed`,
`netchange.detected`, `netchange.unchanged` and `netchange.baseline` (debug),
`netchange.read_failed`, `netchange.push_off`, and from `LogDecision`
`netchange.acted` or `netchange.ignored`. Listed in spec 03, W1 Telemetry.
Interface names and counts only, never addresses.

## Tests

`CGO_ENABLED=0 go test ./internal/agent/wakenet/` (add `-race`). Fake clocks
and injected readings drive both loops; the system tests only read clocks,
interfaces and routes and open and close a push socket. `consts_linux_test.go`
and the netlink system tests run only on Linux.
