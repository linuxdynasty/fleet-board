// Package wakenet tells fleet-agent's connection loop that the machine just
// woke from sleep or that its network really changed, so it can reconnect at
// once instead of waiting for transport keepalive (spec 01 A6, ADR 0029).
//
// Two watchers send [Event] values on a channel the caller owns:
//
//   - [Wake] checks every 5 s. A wake is the time asleep since boot growing by
//     2 s or more (Linux CLOCK_BOOTTIME minus CLOCK_MONOTONIC, macOS
//     CLOCK_MONOTONIC_RAW minus CLOCK_UPTIME_RAW), or the wall clock moving 2 s
//     or more past Go's monotonic clock. Both in one check are one event.
//   - [Net] treats each routing push message (macOS PF_ROUTE, Linux
//     NETLINK_ROUTE) as a hint only. After 500 ms with no further hint, and
//     never more than 2 s after the first, it reads the interfaces that are up
//     and running, their addresses and the default routes, and sends an event
//     only when they differ from the last reading. With the push socket off it
//     polls every 15 s and [Net.PushOff] says so.
//
// [Decide] is the policy for one event and the connection's state: whether to
// reset the backoff and reconnect, whether to close the live stream first, and
// the CONNECT_REASON for Hello. Detection only makes reconnecting faster;
// keepalive and the snapshot on connect keep the agent correct without it.
//
// No cgo and no exec: every system call goes through golang.org/x/sys or the
// standard library, and every clock, reader and socket is a field a test
// replaces.
package wakenet
