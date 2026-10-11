package conn_test

import (
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// stopPoint stops a run at the nth report of one loop step: the step hook,
// on Run's goroutine, waits there until the test has cancelled the run, so
// the stop lands exactly at that point and nowhere else.
type stopPoint struct {
	at, released chan struct{}
	seen         atomic.Int32
}

func stopAtStep(o *conn.LoopOptions, step string, nth int32) *stopPoint {
	p := &stopPoint{at: make(chan struct{}), released: make(chan struct{})}
	conn.SetStepHook(o, func(got string) {
		if got != step || p.seen.Add(1) != nth {
			return
		}
		close(p.at)
		<-p.released
	})
	return p
}

// stop waits for the run to reach the step, cancels it and lets it go on.
func (p *stopPoint) stop(t *testing.T, r *loopRun) {
	t.Helper()
	select {
	case <-p.at:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop never reached the step to stop at")
	}
	r.cancel()
	close(p.released)
}

// saidShutdown reports whether stream n carried Bye{SHUTDOWN}.
func saidShutdown(h *connectHub, n int) bool {
	for _, m := range h.stream(n).msgs {
		if m.GetBye().GetReason() == agentv1.ByeReason_BYE_REASON_SHUTDOWN {
			return true
		}
	}
	return false
}

// Review P1: a stop while a paced backlog drains still ends with
// Bye{SHUTDOWN}. The event case of serve's select must not fail on the
// stopped run's context and end the stream without the goodbye. The stop
// lands just before the fifth ring read: past the burst of 64, in the
// paced part of a 300-event drain.
func TestAStopWhileEventsDrainStillSaysBye(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	fillRing(t, a, 300)
	var p *stopPoint
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Emitter = a.emitter(t)
		p = stopAtStep(o, "events_next", 5)
	})
	p.stop(t, r)
	if err := r.result(t, 10*time.Second); err != nil {
		t.Fatalf("Run returned %v after a stop", err)
	}
	if _, _, evs, _ := streamEvents(h, 1); len(evs) < 64 || len(evs) >= 300 {
		t.Errorf("the stream sent %d events before the stop, want the burst of 64 and part of the rest", len(evs))
	}
	if !saidShutdown(h, 1) {
		t.Errorf("the hub got no Bye{SHUTDOWN} for a stop while events were draining (connect.closed lines: %d)",
			len(a.events(t, "connect.closed")))
	}
}

// Review P2-1: a stop right after the stream is Connected and before the
// event cursor attaches (its floor trim is an agent.db write) still ends
// with Bye{SHUTDOWN}. The attach must use the stream's context: on the
// stopped run's the trim fails, and a stopped run logs no failed attempt,
// so the hub would see the stream drop with no goodbye and nothing logged.
func TestAStopAsEventsAttachStillSaysBye(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	fillRing(t, a, 10)
	var p *stopPoint
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Emitter = a.emitter(t)
		p = stopAtStep(o, "connected", 1)
	})
	p.stop(t, r)
	if err := r.result(t, 10*time.Second); err != nil {
		t.Fatalf("Run returned %v after a stop", err)
	}
	if !saidShutdown(h, 1) {
		t.Errorf("the hub got no Bye{SHUTDOWN} for a stop as the events attached (connect.closed lines: %d)",
			len(a.events(t, "connect.closed")))
	}
}
