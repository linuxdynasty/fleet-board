package conn_test

import (
	"testing"
	"time"
)

// AgentHealth.reconnects_total is read by the heartbeat on the sending
// goroutine, so the loop keeps the count in memory: streams connected after
// the first, as fleet_agent_reconnects_total counts them.
func TestTheLoopCountsItsReconnects(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	if n := r.loop.Reconnects(); n != 0 {
		t.Errorf("Reconnects after the first connect = %d, want 0", n)
	}
	h.dropLive(t)
	h.connected(t, 2)
	waitFor(t, 5*time.Second, "the reconnect to count", func() bool { return r.loop.Reconnects() == 1 })
	h.dropLive(t)
	h.connected(t, 3)
	waitFor(t, 5*time.Second, "the second reconnect to count", func() bool { return r.loop.Reconnects() == 2 })
}
