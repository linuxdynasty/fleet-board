package conn_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// waitChecks waits for n more renewal checks.
func waitChecks(t *testing.T, checks *atomic.Int64, n int64) {
	t.Helper()
	from := checks.Load()
	waitFor(t, 10*time.Second, "renewal checks", func() bool { return checks.Load() >= from+n })
}

// The renewal decision runs on hub time, not the agent's clock: an agent
// clock 75 days fast sends nothing while the hub says the certificate is
// young. Then, against a hub whose signing clock is behind the time its
// HelloAck reports (the new pair is due the moment it is saved), exactly
// one renewal runs, logged as renew.skipped{reason=due_at_once} once,
// never one per check.
func TestAFastAgentClockDoesNotRenewAtEveryCheck(t *testing.T) {
	t.Parallel()
	rh := startRenewHub(t, nil)
	a := rh.enrollAt(t, 0)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	agent, hub := &skewClock{}, &skewClock{}
	agent.set(75 * 24 * time.Hour)
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetClock(o, agent.now)
		conn.SetElapsed(o, hub.since)
		checks = countChecks(o)
	})
	rh.fake.connected(t, 1)
	waitChecks(t, checks, 10)
	if n := len(rh.fake.calls.of("RenewCert")); n != 0 {
		t.Fatalf("%d RenewCert calls with the agent's clock 75 days fast and the hub's certificate young, want 0", n)
	}
	// Hub time passes two thirds; the hub still signs on the real clock.
	hub.set(61 * 24 * time.Hour)
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	// A hundred checks, over a second: room for several more renewals,
	// each of which the hub's writer commits within its 250 ms batch.
	waitChecks(t, checks, 100)
	r.running(t)
	if n := len(rh.Serials(t, id.MachineID)); n != 2 {
		t.Errorf("the hub stored %d serials, want 2: one renewal, not one per check", n)
	}
	if got := a.events(t, "renew.skipped"); len(got) != 1 || got[0]["reason"] != "due_at_once" {
		t.Errorf("renew.skipped = %v, want one due_at_once", got)
	}
}

// After a renewal completes, a reconnect does not start another before a
// check interval has passed, even when the new pair is due by then.
func TestARenewalWaitsOneIntervalAfterTheLast(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	dueHub(h)
	a := enrolled(t, h)
	hub := &skewClock{}
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 2 * time.Second
		conn.SetElapsed(o, hub.since)
	})
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	done := time.Now()
	// Let the renewal goroutine report back to the gate first: it checks
	// whether the new pair is due at once just after renew.completed.
	time.Sleep(100 * time.Millisecond)
	// Hub time moves past the new pair's two thirds, then the stream drops:
	// the next stream's check at connect is due, but within the interval.
	hub.set(62 * 24 * time.Hour)
	h.dropLive(t)
	h.connected(t, 2)
	time.Sleep(200 * time.Millisecond)
	if n := len(h.renewKeys()); n != 1 && time.Since(done) < 2*time.Second {
		t.Fatalf("%d RenewCert calls within the interval after a renewal, want 1", n)
	}
	waitFor(t, 10*time.Second, "the next renewal after the interval", func() bool { return len(h.renewKeys()) == 2 })
}
