package conn_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
)

const (
	resetFirst = 10 * time.Millisecond
	// resetMax lets the doubling reach 2.56 s after 8 failures.
	resetMax = 256 * resetFirst
	// resetSlack covers the time between a failure's log line and the wait.
	resetSlack = 40 * time.Millisecond
)

// failingLoop runs a loop whose every Hello the hub refuses, with a small
// First and a large Max, and returns the channel that feeds it events.
func failingLoop(t *testing.T) (*agent, *loopRun, chan wakenet.Event) {
	t.Helper()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) {
		h.refuse = func(int) error { return connect.NewError(connect.CodeUnavailable, errors.New("busy")) }
	})
	events := make(chan wakenet.Event)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Backoff.First, o.Backoff.Max = resetFirst, resetMax
		o.Events = events
	})
	return a, r, events
}

// waits are the backoff waits before attempts from+1 on (0-based), each
// from a try's connect.failed to the next connect.attempt, and the
// attempts' reasons.
func waits(t *testing.T, a *agent, from, n int) ([]time.Duration, []any) {
	t.Helper()
	waitFor(t, 30*time.Second, "the attempts after the event", func() bool {
		return len(a.events(t, "connect.attempt")) > from+n
	})
	attempts, failed := a.events(t, "connect.attempt"), a.events(t, "connect.failed")
	var out []time.Duration
	var reasons []any
	for i := from; i < from+n; i++ {
		out = append(out, logTime(t, attempts[i+1]).Sub(logTime(t, failed[i])))
		reasons = append(reasons, attempts[i+1]["reason"])
	}
	return out, reasons
}

// inBackoffAfter waits for n failed tries and the backoff after the last.
func inBackoffAfter(t *testing.T, a *agent, r *loopRun, n int) {
	t.Helper()
	waitFor(t, 20*time.Second, "failed tries", func() bool {
		return len(a.events(t, "connect.failed")) >= n && r.loop.Status().State == conn.StateBackoff
	})
}

// ADR 0029, defaults.md "Reconnect backoff": a wake in backoff resets the
// doubling, so the waits after it start again from First (each at most
// First doubled once per failure since the wake), not from where seven
// failures had taken them.
func TestAWakeResetsTheBackoff(t *testing.T) {
	t.Parallel()
	a, r, events := failingLoop(t)
	inBackoffAfter(t, a, r, 7)
	events <- wake
	waitFor(t, 20*time.Second, "the wake's attempt", func() bool {
		for _, at := range a.events(t, "connect.attempt") {
			if at["reason"] == "wake" {
				return true
			}
		}
		return false
	})
	k := -1
	for i, at := range a.events(t, "connect.attempt") {
		if at["reason"] == "wake" {
			k = i
		}
	}
	got, _ := waits(t, a, k, 4)
	for i, d := range got {
		if ceil := resetFirst << (i + 1); d > ceil+resetSlack {
			t.Errorf("wait %d after the wake = %v, want at most %v (the doubling restarted)", i+1, d, ceil)
		}
	}
	r.running(t)
}

// A burst of network events that change nothing (an unchanged routing
// reading) leaves the backoff alone: no try is cut short or made with
// CONNECT_REASON_NETWORK_CHANGE, and the doubling keeps its place.
func TestAnUnchangedRoutingBurstLeavesTheBackoff(t *testing.T) {
	t.Parallel()
	a, r, events := failingLoop(t)
	inBackoffAfter(t, a, r, 7)
	from := len(a.events(t, "connect.failed")) - 1
	unchanged := wakenet.Event{Reason: agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE}
	for range 5 {
		events <- unchanged
	}
	got, reasons := waits(t, a, from, 4)
	reset := true
	for i, d := range got {
		if reasons[i] != "reconnect" {
			t.Errorf("try %d after the burst says %v, want reconnect", i+1, reasons[i])
		}
		if d > (resetFirst<<(i+1))+resetSlack {
			reset = false
		}
	}
	// After 8 or more failures each wait is up to 2.56 s; all four inside
	// a restarted doubling (60, 80, 120, 200 ms) has odds under 3 in a
	// million unless the burst reset it.
	if reset {
		t.Errorf("the waits after the burst %v all fit a restarted doubling", got)
	}
	if n := len(a.events(t, "netchange.ignored")); n != 5 {
		t.Errorf("%d netchange.ignored lines, want 5", n)
	}
	r.running(t)
}
