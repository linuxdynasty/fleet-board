# 0029. Wake and network-change detection

- Status: Accepted
- Date: 2026-10-09
- Deciders: linuxdynasty
- Builds on: [0004](0004-agent-connectivity.md) (snapshot on every connect,
  transport keepalive, reconnect on wake or network change),
  [0020](0020-dependencies.md) (a new runtime dependency needs a line or an
  ADR), [0028](0028-mdns-best-effort.md) (backoff reset and the short random
  delay before the attempt a wake or network change starts)
- Amends: [0004](0004-agent-connectivity.md) ("immediately on wake or network
  change": which network changes reconnect), [0020](0020-dependencies.md)
  (adds `golang.org/x/sys`; ADR 0020 itself is not edited)

## Context

A laptop that sleeps or changes network leaves `fleet-agent` holding a dead
connection. Spec 01 A6 asked the agent to notice and reconnect at once, and
left the method to plan item 0.6 (issue #51). The spike is in
[spikes/wakenet/](../../spikes/wakenet/results/README.md); it ran four methods
on the Mac mini, then for about a day on omarchy (Linux) and the MacBook
(macOS).

- **Network change was seen on both systems.** A VPN interface came up on
  omarchy and went down and up on the MacBook. The routing push socket (a
  kernel socket that sends a message when an interface, address or route
  changes) reported each one within milliseconds, and a 5 s interface poll saw
  the same change 1 to 5 s later.
- **Linux sends many push messages that change nothing.** omarchy logged 972
  "address added" messages, almost all for its wired interface, about one every
  38 s at the median. Its router keeps refreshing an IPv6 address, and each
  refresh re-sends the message. The interface poll saw no change for any of
  them. So a push message alone cannot mean "reconnect".
- **No real sleep was seen.** The owner does not put machines to sleep, and the
  product does not ask for it. One suspend on omarchy was refused by its
  graphics driver within a second; the kernel counted no time asleep.
- **The wake method still rests on documented rules.** Go's monotonic clock is
  `mach_absolute_time` on macOS and `CLOCK_MONOTONIC` on Linux; both stop while
  the machine sleeps, and the wall clock does not. Apple's `mach_time.h` and the
  Linux `clock_gettime(2)` page say so, and Go's `time` package warns that "the
  monotonic clock will stop if the computer goes to sleep". The kernel also
  keeps its own record of sleep: `kern.waketime` on macOS and the difference
  `CLOCK_BOOTTIME` minus `CLOCK_MONOTONIC` on Linux. Sources and line numbers
  are in the spike results.
- **`golang.org/x/sys` is already a direct requirement** of the product
  (v0.48.0 in `go.mod`), used by the hub's peer-credential check
  (`internal/hub/api/peercred_*.go`) and terminal code
  (`internal/hub/backup/`), with no ADR line until this one.

## Options

**Wake:**

1. Compare how far the wall clock moved with how far the monotonic clock moved,
   on a timer. No dependency; a wall-clock step (NTP, a manual change) can look
   like a wake.
2. Read the kernel's sleep record on a timer. Exact, immune to clock steps,
   needs `x/sys`.
3. logind `PrepareForSleep` over D-Bus (Linux). An exact push, but needs
   `github.com/godbus/dbus/v5`. Rejected.
4. IOKit power notifications (macOS). Needs cgo or `purego`. Rejected.

**Network change:**

1. Act on every routing push message. Rejected: the Linux noise above would
   reconnect every minute or so.
2. Treat a push message as a hint, then read the interface, address and
   default-route list and act only when it really changed.
3. Poll the interface list on a timer as the main method. Rejected: it costs
   about 3.5 times the push socket on the Mac mini and still needs the same
   comparison.
4. Read the macOS default route by running `route -n get default`. Rejected:
   no `exec` in this code, and the exec doubled the poll's cost.
5. `golang.org/x/net/route` for the macOS routing table. Rejected without
   another ADR line; `x/sys` already reaches the same sysctl.

## Decision

1. **Item 0.6 is done as a recorded waiver.** The network-change half is proven
   by real changes on both systems. The wake half rests on the documented clock
   rules above and on fake-clock tests in plan row 4. A real sleep is not a
   merge gate; the first time a laptop sleeps on its own, its log is checked
   (#132).
2. **Wake and network-change detection only makes reconnecting faster.** It is
   not needed for correctness. A missed wake costs at most about 30 s, because
   transport keepalive (ADR 0004: a ping every 10 s, a 20 s timeout) notices the
   dead connection; a false wake costs one reconnect; the snapshot on every
   connect (ADR 0004, Reliability) puts the state right either way. This holds
   only if row 4 builds that transport keepalive, so row 4 now names it, and the
   hub's keepalive enforcement (row 5) must accept a ping every 10 s.
3. **Wake: one check every 5 s with two signals.** The clock signal fires when
   the wall clock moved 2 s or more past the monotonic clock since the last
   check. A wall clock stepped back is logged, never a wake. The sleep-record
   signal fires when Linux's suspended time (`CLOCK_BOOTTIME` minus
   `CLOCK_MONOTONIC`) grew by 2 s or more, or when macOS `kern.waketime`
   changed. Either signal, or both, starts one reconnect per check, even while
   connected: close the stream and reconnect with `CONNECT_REASON_WAKE`.
4. **Network change: the push socket is only a hint.** macOS `PF_ROUTE` and
   Linux `NETLINK_ROUTE` need no root. After a hint the agent waits until 500 ms
   pass with no further hint, but never more than 2 s after the first, then
   reads the interfaces that are up and running, their addresses and the default
   route, and compares them with the last reading. No difference, no action.
   - Not connected: a real difference resets the backoff and starts an attempt
     after the short random delay (ADR 0028), with
     `CONNECT_REASON_NETWORK_CHANGE`. The 5 s minimum between connects still
     applies.
   - Connected: only two differences reconnect: the connection's own local
     address is gone from every interface that is up, or the default route
     changed.
   - On Linux, a full socket buffer (`ENOBUFS`) counts as a hint.
5. **Interface poll is only a fallback.** Every 15 s, and only when the push
   socket cannot be opened or stops working. The agent logs that once and
   `fleet-agent doctor` reports it. There is no always-on poll loop.
6. **No `exec` and no cgo.** The macOS default route comes either from the
   routing table read through `x/sys` or from default-route push messages; row 4
   picks one and proves it with a fixture test. The spike's `route` exec is
   spike-only. `x/net/route`, `godbus/dbus` and `purego` are not allowed for
   this without another ADR.
7. **Dependency line.** `golang.org/x/sys` is allowed for the agent's wake and
   network-change code (sysctl, `clock_gettime`, routing and netlink sockets)
   and for the hub's existing uses (peer credentials, terminal modes). It is
   maintained by the Go team and needs no cgo.

Numbers are in [defaults](../specs/defaults.md); the rule is in
[01](../specs/01-agent-capabilities.md) A6 and [03](../specs/03-workflows.md)
A2.

## Consequences

- Plan row 4 builds transport keepalive along with wake and network-change
  detection, and its acceptance test covers both with a fake clock and injected
  readings, including the recorded omarchy IPv6 refresh never reconnecting.
- Plan row 5's gateway must allow the agent's 10 s ping without closing the
  stream with `too_many_pings` ([04](../specs/04-technology-review.md)).
- A command that arrived before a sleep must not start after the wake once its
  deadline, counted with the time asleep, has passed. That is a separate fix
  (#133); row 6 waits for it.
- The spike's 500 ms sleep-record tolerance becomes 2 s in product code.
- Row 4 may start now but merges only after the 0.6 results.

## Open questions

1. Does a real sleep behave as the documents say? Not run, and no machine is
   put to sleep for it. The first natural laptop sleep with `wakenet` or
   `fleet-agent` running answers it (#132). Unknown until then: a macOS dark
   wake (Power Nap), Linux `s2idle`, and how late Go's timer fires after a wake.
