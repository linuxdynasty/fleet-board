package conn

import (
	"net"
	"time"
)

// SetAnchorHook makes the loop report each stream's hub-time anchor (when
// its HelloAck arrived), for tests that check it keeps a monotonic reading.
func SetAnchorHook(o *LoopOptions, f func(hubRecv time.Time)) { o.anchorHook = f }

// SetMDNSLimits shortens the loop's mDNS budgets and lets answers on
// loopback through, where the test hubs listen.
func SetMDNSLimits(o *LoopOptions, try, phase time.Duration) {
	o.mdns = mdnsLimits{try: try, phase: phase, loopbackOK: true}
}

// SetEnrollMDNSLimits is SetMDNSLimits for Enroll.
func SetEnrollMDNSLimits(o *Options, try, phase time.Duration) {
	o.mdns = mdnsLimits{try: try, phase: phase, loopbackOK: true}
}

// NewMDNSOn is NewMDNS on one multicast interface, for the live test.
func NewMDNSOn(ifi *net.Interface) Discover {
	m := newMDNS()
	m.iface = ifi
	return m
}
