package conn_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// stopWithin cancels the loop and fails the test unless Run returns nil
// within limit.
func stopWithin(t *testing.T, r *loopRun, limit time.Duration) {
	t.Helper()
	start := time.Now()
	r.cancel()
	if err := r.result(t, 30*time.Second); err != nil {
		t.Errorf("Run after cancel = %v, want nil", err)
	}
	if took := time.Since(start); took > limit {
		t.Errorf("Run returned %v after the cancel, want within %v", took, limit)
	}
}

// A hub that answers pings but stops reading leaves a send blocked by
// HTTP/2 flow control; a stop still ends the loop within the shutdown
// wait (2 s), and the connection is closed.
func TestAStalledSendDoesNotHoldUpAStop(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) { h.deaf = true })
	// Two of these overflow the hub's 1 MiB receive window.
	pad := strings.Repeat("x", 900<<10)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(_ context.Context, s *agentv1.Snapshot) error {
			s.Facts.Os = pad
			return nil
		}
	})
	r.connected(t)
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{
		SnapshotRequest: &agentv1.SnapshotRequest{Reason: "test"},
	}})
	waitFor(t, 5*time.Second, "the second snapshot to start", func() bool {
		return len(a.events(t, "snapshot.requested")) == 1
	})
	time.Sleep(300 * time.Millisecond)
	if n := len(a.events(t, "snapshot.sent")); n != 1 {
		t.Fatalf("%d snapshot.sent lines, want 1 (the second stalled)", n)
	}
	stopWithin(t, r, 4*time.Second)
	waitFor(t, 5*time.Second, "the hub's connection to close", func() bool { return h.open.Load() == 0 })
	if cut := a.events(t, "connect.shutdown_cut"); len(cut) != 1 || cut[0]["peer"] != h.addr {
		t.Errorf("connect.shutdown_cut = %v, want one for %s", cut, h.addr)
	}
}

// A stop while an attempt is still dialing ends at once and is not counted
// or logged as a failed attempt.
func TestAStopDuringAnAttemptIsNotAFailure(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	only := onlyAddress(t, a, silentAddr(t))
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		only(o)
		o.HandshakeTimeout = time.Minute
	})
	waitFor(t, 5*time.Second, "the attempt", func() bool { return len(a.events(t, "connect.attempt")) == 1 })
	time.Sleep(100 * time.Millisecond)
	// The attempt would otherwise run its minute-long handshake timeout.
	stopWithin(t, r, 10*time.Second)
	if got := metric(scrape(t, a), "fleet_agent_connect_attempts_total", `result="failed"`); got != "" {
		t.Errorf(`connect_attempts_total{result="failed"} = %s after a stop, want none`, got)
	}
	if got := a.events(t, "connect.failed"); len(got) != 0 {
		t.Errorf("connect.failed = %v after a stop, want none", got)
	}
}
