package wakenet

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"testing"
	"time"
)

var errDumpRead = errors.New("route dump: no buffer space")

// withTun0 is the home reading with a VPN interface up.
func withTun0() Reading {
	r := homeReading()
	r.Interfaces["tun0"] = []netip.Prefix{pfx("10.8.0.2/24")}
	return r
}

func wantTimer(t *testing.T, tm *fakeTimer, d time.Duration, what string) {
	t.Helper()
	if tm.d != d {
		t.Fatalf("%s: wait %v, want %v", what, tm.d, d)
	}
}

// TestFailedPushReadIsRetried: with the push socket on, a failed read is tried
// again 2 s later without waiting for another hint, so the change that caused
// the hint is not missed.
func TestFailedPushReadIsRetried(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	g.reader.set(Reading{}, errDumpRead)
	if got := g.settle(t, g.hint(t, 0), 500*time.Millisecond); got != "netchange.read_failed" {
		t.Fatalf("got %s, want netchange.read_failed", got)
	}
	retry := g.clock.nextTimer(t)
	wantTimer(t, retry, 2*time.Second, "retry after a failed read")

	g.reader.set(withTun0(), nil)
	if got := g.settle(t, retry, 2500*time.Millisecond); got != "netchange.detected" {
		t.Fatalf("retry: %s, want netchange.detected", got)
	}
	ev := <-g.out
	if len(ev.Net.Changes) != 1 || ev.Net.Changes[0] != (Change{Kind: InterfaceAdded, Iface: "tun0"}) {
		t.Errorf("changes = %+v, want tun0 added against the first baseline", ev.Net.Changes)
	}
	if n := g.logs.count("netchange.read_recovered"); n != 1 {
		t.Errorf("netchange.read_recovered logged %d times, want 1", n)
	}
}

// TestRepeatedPushReadFailures: the retry keeps going every 15 s while reads
// fail, and the failure is logged once, then once more on recovery.
func TestRepeatedPushReadFailures(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	g.reader.set(Reading{}, errDumpRead)
	g.settle(t, g.hint(t, 0), 500*time.Millisecond)

	retry := g.clock.nextTimer(t)
	wantTimer(t, retry, 2*time.Second, "first retry")
	g.clock.setMono(2500 * time.Millisecond)
	retry.fire()
	retry = g.clock.nextTimer(t)
	wantTimer(t, retry, 15*time.Second, "second retry")

	g.reader.set(homeReading(), nil)
	if got := g.settle(t, retry, 17500*time.Millisecond); got != "netchange.unchanged" {
		t.Fatalf("recovered read: %s, want netchange.unchanged", got)
	}
	if n := g.logs.count("netchange.read_failed"); n != 1 {
		t.Errorf("netchange.read_failed logged %d times over 2 failures, want 1", n)
	}
	if v, ok := g.logs.attr("netchange.read_recovered", "failures"); !ok || v.Int64() != 2 {
		t.Errorf("netchange.read_recovered failures = %v (logged %v), want 2", v, ok)
	}
}

func TestFailedStartReadIsRetried(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, Reading{}, errors.New("no interfaces"), nil)
	g.logs.waitFor(t, "netchange.read_failed")
	retry := g.clock.nextTimer(t)
	wantTimer(t, retry, 2*time.Second, "retry after a failed start reading")
	g.reader.set(homeReading(), nil)
	if got := g.settle(t, retry, 2*time.Second); got != "netchange.baseline" {
		t.Fatalf("got %s, want netchange.baseline", got)
	}
	if evs := drain(g.out); len(evs) != 0 {
		t.Errorf("events = %+v, want none: there was nothing to compare with", evs)
	}
}

// TestPollReadFailuresLogOnce: in poll mode a failing read is logged once and
// its recovery once, with the count.
func TestPollReadFailuresLogOnce(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, errors.New("socket: not supported"))
	g.logs.waitFor(t, "netchange.baseline")
	g.reader.set(Reading{}, errDumpRead)
	poll := g.clock.nextTimer(t)
	if got := g.settle(t, poll, 15*time.Second); got != "netchange.read_failed" {
		t.Fatalf("first poll: %s, want netchange.read_failed", got)
	}
	poll = g.clock.nextTimer(t)
	wantTimer(t, poll, 15*time.Second, "poll after a failure")
	g.clock.setMono(30 * time.Second)
	poll.fire()
	poll = g.clock.nextTimer(t)

	g.reader.set(withTun0(), nil)
	if got := g.settle(t, poll, 45*time.Second); got != "netchange.detected" {
		t.Fatalf("third poll: %s, want netchange.detected", got)
	}
	if n := g.logs.count("netchange.read_failed"); n != 1 {
		t.Errorf("netchange.read_failed logged %d times over 2 failures, want 1", n)
	}
	if v, ok := g.logs.attr("netchange.read_recovered", "failures"); !ok || v.Int64() != 2 {
		t.Errorf("netchange.read_recovered failures = %v (logged %v), want 2", v, ok)
	}
}

func TestDeadPushSocketIsClosed(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	g.sock.errs <- errors.New("read netlink: bad file descriptor")
	wantTimer(t, g.clock.nextTimer(t), 15*time.Second, "poll after the socket died")
	select {
	case <-g.sock.closed:
	case <-time.After(waitLimit):
		t.Fatal("the dead push socket was never closed")
	}
}

// TestPushOffIsPerRun: a later Run whose push socket opens reports it on.
func TestPushOffIsPerRun(t *testing.T) {
	t.Parallel()
	clock, reader, logs := newFakeClock(), &fakeReader{}, newLogCapture()
	reader.set(homeReading(), nil)
	opens := 0
	n := &Net{
		Clock: clock, Reader: reader, Hint: netlinkHint, Log: logs.logger(),
		OpenPush: func() (io.ReadCloser, error) {
			opens++
			if opens == 1 {
				return nil, errors.New("socket: not supported")
			}
			return newFakeSocket(), nil
		},
	}
	out := make(chan Event, 4)
	stop := runLoop(t, func(ctx context.Context) { n.Run(ctx, out) })
	logs.waitFor(t, "netchange.baseline")
	if !n.PushOff() {
		t.Fatal("PushOff() = false with the socket refused")
	}
	stop()

	runLoop(t, func(ctx context.Context) { n.Run(ctx, out) })
	logs.waitFor(t, "netchange.baseline")
	if n.PushOff() {
		t.Error("PushOff() = true in a Run whose push socket opened")
	}
}

// TestNoPollWhilePushWorks: with the push socket on, the first timer the
// watcher asks for is the debounce of the first hint, never a poll.
func TestNoPollWhilePushWorks(t *testing.T) {
	t.Parallel()
	g := newNetRig(t, homeReading(), nil, nil)
	g.logs.waitFor(t, "netchange.baseline")
	wantTimer(t, g.hint(t, 0), 500*time.Millisecond, "first timer with the push socket on")
}
