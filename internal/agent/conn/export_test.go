package conn

import (
	"net"
	"time"
)

// QueueSize is the per-stream outbound queue's room.
const QueueSize = queueSize

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

// SetClock replaces the loop's wall clock for the certificate's expiry and
// renewal decisions.
func SetClock(o *LoopOptions, now func() time.Time) { o.clock = now }

// SetElapsed replaces the monotonic time since a moment (time.Since), from
// which the loop measures hub time after a HelloAck, so a test can move hub
// time on without a new stream.
func SetElapsed(o *LoopOptions, since func(t time.Time) time.Duration) { o.elapsed = since }

// SetSleepClock replaces the clock that keeps counting while the machine
// sleeps, from which hub time also measures the time since a HelloAck.
func SetSleepClock(o *LoopOptions, read func() (time.Duration, error)) { o.sleepClock = read }

// SetStepHook makes the loop report its queue steps ("connected" right
// after the status says Connected, "unpublished" right after a stream's
// queue closed, "unacked_read" just before a snapshot reads the unacked
// results), on Run's goroutine.
func SetStepHook(o *LoopOptions, f func(step string)) { o.stepHook = f }

// SetHookWarn shortens the wait before connect.hook_blocked.
func SetHookWarn(o *LoopOptions, d time.Duration) { o.hookWarn = d }

// SetRenewHook makes the loop report each renewal step ("before_send",
// "after_answer", "after_rename"), each check ("check") and the renewal
// goroutine's report to the gate ("reported"); returning true stops the
// renewal there as a crash would.
func SetRenewHook(o *LoopOptions, f func(step string) bool) { o.renewHook = f }
