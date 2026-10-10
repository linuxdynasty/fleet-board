package conn_test

import (
	"errors"
	"math"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// sleepClock is a fake clock that keeps counting while the machine
// sleeps: it reads the time since a start, moved on by an offset.
type sleepClock struct {
	start time.Time
	off   atomic.Int64
}

func newSleepClock() *sleepClock { return &sleepClock{start: time.Now()} }

func (c *sleepClock) read() (time.Duration, error) {
	return time.Since(c.start) + time.Duration(c.off.Load()), nil
}

// sleep moves the clock on by d, as a sleep of d would (Go's monotonic
// clock does not move).
func (c *sleepClock) sleep(d time.Duration) { c.off.Add(int64(d)) }

// Hub time counts the time the machine slept: after the HelloAck, a sleep
// of 61 days (on the sleep-counting clock; Go's monotonic clock stood
// still) makes the enrolled pair due, the expiry gauge reads 29 days left,
// and after 91 days the next attempt ends the loop as expired.
func TestHubTimeCountsTheTimeAsleep(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) {
		h.renew.fail = func(int) error { return connect.NewError(connect.CodeUnavailable, errNoRenew) }
	})
	a := enrolled(t, h)
	leaf := enrolledLeaf(t, a)
	clock := newSleepClock()
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetSleepClock(o, clock.read)
		checks = countChecks(o)
	})
	h.connected(t, 1)
	waitChecks(t, checks, 5)
	if n := len(h.renewKeys()); n != 0 {
		t.Fatalf("%d RenewCert calls before any sleep, want 0", n)
	}
	clock.sleep(61 * 24 * time.Hour)
	waitFor(t, 10*time.Second, "a renewal after 61 days asleep", func() bool { return len(h.renewKeys()) >= 1 })
	exp, err := strconv.ParseFloat(metric(scrape(t, a), "fleet_agent_cert_expiry_seconds"), 64)
	want := leaf.NotAfter.Sub(time.Now().Add(61 * 24 * time.Hour)).Seconds()
	if err != nil || math.Abs(exp-want) > 60 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v (%v), want about %v counting the sleep", exp, err, want)
	}
	clock.sleep(30 * 24 * time.Hour)
	h.dropLive(t)
	err = r.result(t, 10*time.Second)
	if ended, ok := errors.AsType[*conn.EndedError](err); !ok || ended.End != conn.EndCertExpired {
		t.Fatalf("Run = %v, want EndCertExpired after 91 days asleep", err)
	}
}

// A pair marked due_at_once (a hub whose HelloAck clock ran 61 days ahead
// of the clock it signed with) is not renewed while it still looks due,
// but once a correct HelloAck shows it young the mark clears, and when it
// is truly due it is renewed; the loop does not end at the old pair's end.
func TestADueAtOnceMarkClearsWhenThePairIsYoungAgain(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	dueHub(h)
	// The hub signs on the real clock (two seconds on, so the answer ends a
	// whole second after the enrolled pair, which was signed just now).
	h.set(func(h *connectHub) { h.renew.issueAt = func() time.Time { return time.Now().Add(2 * time.Second) } })
	a := enrolled(t, h)
	hub := &skewClock{}
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetElapsed(o, hub.since)
		checks = countChecks(o)
	})
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	waitFor(t, 10*time.Second, "due_at_once", func() bool { return len(a.events(t, "renew.skipped")) == 1 })
	waitChecks(t, checks, 10)
	if n := len(h.renewKeys()); n != 1 {
		t.Fatalf("%d RenewCert calls while the hub's clocks disagree, want 1", n)
	}
	// The hub's clock is fixed; the next stream's HelloAck shows the pair
	// young.
	h.set(func(h *connectHub) { h.hubClock = nil })
	h.dropLive(t)
	h.connected(t, 2)
	waitChecks(t, checks, 10)
	if n := len(h.renewKeys()); n != 1 {
		t.Fatalf("%d RenewCert calls with a correct hub clock and a young pair, want 1", n)
	}
	// 61 days pass on the hub, which now signs on the same clock.
	h.set(func(h *connectHub) {
		h.renew.issueAt = func() time.Time { return time.Now().Add(61 * 24 * time.Hour) }
	})
	hub.set(61 * 24 * time.Hour)
	waitFor(t, 10*time.Second, "the renewal of the truly due pair", func() bool {
		return len(a.events(t, "renew.completed")) == 2
	})
	// 91 days: past the first renewed pair's end, not the second's.
	hub.set(91 * 24 * time.Hour)
	h.dropLive(t)
	h.connected(t, 3)
	r.running(t)
}

// After a HelloAck with no hub_time the renewal decision falls back to
// the agent's clock, not the previous stream's anchor (amendment c).
func TestNoHubTimeFallsBackToTheAgentClock(t *testing.T) {
	t.Parallel()
	h := startConnectHubWith(t, func(h *connectHub) {
		h.tamperAck = func(n int, ack *agentv1.HelloAck) {
			if n >= 2 {
				ack.HubTime = nil
			}
		}
	})
	a := enrolled(t, h)
	var checks *atomic.Int64
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetClock(o, renewDueClock)
		checks = countChecks(o)
	})
	h.connected(t, 1)
	waitChecks(t, checks, 10)
	if n := len(h.renewKeys()); n != 0 {
		t.Fatalf("%d RenewCert calls while the hub says the pair is young", n)
	}
	h.dropLive(t)
	h.connected(t, 2)
	waitFor(t, 10*time.Second, "a renewal on the agent's clock", func() bool { return len(h.renewKeys()) >= 1 })
}
