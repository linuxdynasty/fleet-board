package conn_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// clockRig drives a loop's monotonic and sleep-counting clocks: mono and
// asleep are added to the real time since, so a test "sleeps" the machine
// by raising asleep alone (Go's monotonic clock stops in sleep).
type clockRig struct {
	mono, asleep atomic.Int64
	broken       atomic.Bool
	base         time.Time
}

func (c *clockRig) set(o *conn.LoopOptions) {
	c.base = time.Now()
	conn.SetElapsed(o, func(t time.Time) time.Duration { return time.Since(t) + time.Duration(c.mono.Load()) })
	conn.SetSleepClock(o, func() (time.Duration, error) {
		if c.broken.Load() {
			return 0, errors.New("the sleep clock cannot be read")
		}
		return time.Hour + time.Since(c.base) + time.Duration(c.asleep.Load()), nil
	})
}

// seen is what the hooks handed over: the clock and each command.
type seen struct {
	mu    sync.Mutex
	clock conn.HubClock
	cmds  []*agentv1.Command
}

func (s *seen) hooks(o *conn.LoopOptions) {
	o.OnConnected = func(_ uint64, c conn.HubClock) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.clock = c
	}
	o.Command = func(_ uint64, c *agentv1.Command) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cmds = append(s.cmds, c)
		return true
	}
}

func (s *seen) get() (conn.HubClock, []*agentv1.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clock, append([]*agentv1.Command(nil), s.cmds...)
}

// Plan row 6 acceptance (ruling fork 11): a command that arrived before a
// sleep the kernel recorded is not started after the wake once its
// deadline, counted with the time asleep, has passed.
func TestACommandFromBeforeASleepIsPastItsDeadlineAfterTheWake(t *testing.T) {
	t.Parallel()
	hubT := time.Now().Add(-time.Minute).Truncate(time.Second)
	h := startConnectHubWith(t, func(h *connectHub) { h.hubClock = func() time.Time { return hubT } })
	a := enrolled(t, h)
	var rig clockRig
	var s seen
	startLoop(t, a, func(o *conn.LoopOptions) {
		rig.set(o)
		s.hooks(o)
	})
	h.connected(t, 1)
	notAfter := hubT.Add(10 * time.Minute)
	h.pushCommand(t, "c1", notAfter)
	waitFor(t, 5*time.Second, "the command", func() bool { _, c := s.get(); return len(c) == 1 })
	clock, cmds := s.get()
	deadline := cmds[0].GetNotAfterTime().AsTime()
	now, ok := clock.Now()
	if !ok || !now.Before(deadline) || now.Sub(hubT) > time.Minute {
		t.Fatalf("before the sleep: hub time %v (ok %v), want inside the deadline %v", now, ok, deadline)
	}
	// Two hours asleep: the monotonic clock did not count them.
	rig.asleep.Add(int64(2 * time.Hour))
	now, ok = clock.Now()
	if !ok || !now.After(deadline) {
		t.Errorf("after the wake: hub time %v (ok %v), want past the deadline %v", now, ok, deadline)
	}
	if now.Sub(hubT) < 2*time.Hour {
		t.Errorf("after the wake hub time is %v past hub_time, want the two hours asleep counted", now.Sub(hubT))
	}
	// A sleep clock that cannot be read makes every deadline past.
	rig.broken.Store(true)
	if now, ok := clock.Now(); ok {
		t.Errorf("with the sleep clock unreadable Now() = %v, true; want not known", now)
	}
}

// A later HelloAck refines the anchor; one with no hub_time leaves the
// clock not known until the next one that has it.
func TestTheStrictClockFollowsTheLatestHelloAck(t *testing.T) {
	t.Parallel()
	first := time.Now().Add(-time.Minute).Truncate(time.Second)
	h := startConnectHubWith(t, func(h *connectHub) {
		h.tamperAck = func(n int, ack *agentv1.HelloAck) {
			switch n {
			case 1:
				ack.HubTime.Seconds = first.Unix()
				ack.HubTime.Nanos = 0
			case 2:
				ack.HubTime = nil
			}
		}
	})
	a := enrolled(t, h)
	var rig clockRig
	var s seen
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		rig.set(o)
		s.hooks(o)
	})
	h.connected(t, 1)
	clock := r.loop.HubClock()
	if now, ok := clock.Now(); !ok || now.Sub(first).Abs() > 30*time.Second {
		t.Errorf("after stream 1: %v, %v; want about %v", now, ok, first)
	}
	h.dropLive(t)
	h.connected(t, 2)
	if now, ok := clock.Now(); ok {
		t.Errorf("after a HelloAck with no hub_time: %v, true; want not known", now)
	}
	h.dropLive(t)
	h.connected(t, 3)
	if now, ok := clock.Now(); !ok || time.Until(now).Abs() > 30*time.Second {
		t.Errorf("after stream 3: %v, %v; want about the hub's clock now", now, ok)
	}
}

// Before the first HelloAck of the run there is no hub time at all.
func TestTheStrictClockIsNotKnownBeforeTheFirstHelloAck(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) { h.refuse = func(int) error { return errors.New("not now") } })
	r := startLoop(t, a, nil)
	waitFor(t, 5*time.Second, "a refused stream", func() bool { return h.count() >= 1 })
	if now, ok := r.loop.HubClock().Now(); ok {
		t.Errorf("before any HelloAck Now() = %v, true; want not known", now)
	}
}
