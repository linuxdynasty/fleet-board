package conn_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// realHubAgent enrolls a fresh agent with the real hub (conntest, plan
// row 5 PR 5a-ii: the gateway's own Connect) and starts its loop.
func realHubAgent(t *testing.T, h *conntest.Hub, set func(*conn.LoopOptions)) (*agent, *loopRun, string) {
	t.Helper()
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, startLoop(t, a, set), id.MachineID
}

// agentEpoch is the epoch agent.db holds (the last HelloAck.epoch taken).
func agentEpoch(t *testing.T, a *agent) uint64 {
	t.Helper()
	st, err := a.openDB(t).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st.Epoch
}

// The agent's real connection loop reaches Connected through the hub's
// real Connect: the hub stores epoch 1 in one live machine_connections row
// and the agent saves the same epoch.
func TestLoopConnectsToTheRealHub(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a, r, id := realHubAgent(t, h, nil)
	r.connected(t)
	if got := h.LastEpoch(t, id); got != 1 {
		t.Errorf("hub last_epoch = %d, want 1", got)
	}
	if got := h.Connections(t, id); len(got) != 1 || got[0].Epoch != 1 || got[0].EndReason != "" {
		t.Errorf("machine_connections = %+v, want one live row at epoch 1", got)
	}
	if got := agentEpoch(t, a); got != 1 {
		t.Errorf("agent.db epoch = %d, want 1", got)
	}
	if n := len(h.Events(t, "reconnect.hello")); n != 1 {
		t.Errorf("%d reconnect.hello lines, want 1", n)
	}
}

// A graceful hub restart says Bye{HUB_RESTART}; the agent reconnects to
// the new hub process on the same state.db and gets epoch 2, stored on
// both sides, and the first row is closed as hub_restart.
func TestLoopReconnectsAfterTheRealHubRestarts(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a, r, id := realHubAgent(t, h, nil)
	r.connected(t)
	h.Restart(t)
	waitFor(t, 10*time.Second, "epoch 2 on the hub", func() bool { return h.LastEpoch(t, id) == 2 })
	r.connected(t)
	waitFor(t, 10*time.Second, "epoch 2 in agent.db", func() bool { return agentEpoch(t, a) == 2 })
	got := h.Connections(t, id)
	if len(got) != 2 || got[0].EndReason != "hub_restart" || got[1].Epoch != 2 || got[1].EndReason != "" {
		t.Errorf("machine_connections = %+v, want epoch 1 hub_restart, epoch 2 live", got)
	}
	byes := a.events(t, "connect.bye")
	if len(byes) != 1 || byes[0]["reason"] != "HUB_RESTART" {
		t.Errorf("connect.bye lines %v, want one HUB_RESTART", byes)
	}
	r.running(t)
}

// A hub that shuts down while the agent's Hello waits for the store says
// Bye{HUB_RESTART} before any HelloAck. The real loop takes it as a failed
// attempt (connect.bye, then backoff), reconnects to the hub's next
// process and reaches Connected. The Hello job of the first process still
// commits during its Close, leaving a row no stream holds; the next Hello
// closes it as displaced. With one certificate pair no pair switch is
// possible, so renew.pair_switched is not logged.
func TestLoopReconnectsAfterAByeBeforeHelloAck(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	free := h.HoldWriter(t)
	before := h.Requests()
	r := startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the agent's Connect", func() bool { return h.Requests() > before })
	time.Sleep(300 * time.Millisecond) // setup: the Hello reaches its job wait
	go func() {
		time.Sleep(500 * time.Millisecond) // Restart's Close waits for the held job
		free()
	}()
	h.Restart(t)
	r.connected(t)
	byes := a.events(t, "connect.bye")
	if len(byes) != 1 || byes[0]["reason"] != "HUB_RESTART" {
		t.Errorf("connect.bye lines %v, want one HUB_RESTART", byes)
	}
	if n := len(a.events(t, "renew.pair_switched")); n != 0 {
		t.Errorf("%d renew.pair_switched lines, want 0 with one pair", n)
	}
	got := h.Connections(t, id.MachineID)
	if len(got) != 2 || got[0].Epoch != 1 || got[0].EndReason != "displaced" || got[1].Epoch != 2 || got[1].EndReason != "" {
		t.Errorf("machine_connections = %+v, want epoch 1 (left open by the shutdown) displaced, epoch 2 live", got)
	}
	r.running(t)
}

// keepaliveSurvives connects with ka and checks the stream is still the
// first one after hold. After HelloAck the hub sends nothing on this
// stream but HTTP/2 ping answers (the few hundred bytes the agent sends
// stay far below half a flow-control window, so no window update either).
// The agent pings after ka.Ping with nothing received and closes the
// connection when no answer comes within ka.Timeout: an unanswered k-th
// ping ends the stream by k*ka.Ping + ka.Timeout, so a stream alive after
// 3*ka.Ping + ka.Timeout had at least three pings answered.
func keepaliveSurvives(t *testing.T, ka conn.Keepalive, hold time.Duration) {
	t.Helper()
	h := conntest.StartHub(t, conntest.Options{})
	a, r, id := realHubAgent(t, h, func(o *conn.LoopOptions) { o.Keepalive = ka })
	r.connected(t)
	if hold <= 3*ka.Ping+ka.Timeout {
		t.Fatalf("hold %v proves fewer than three pings", hold)
	}
	time.Sleep(hold)
	r.running(t)
	if closed := a.events(t, "connect.closed"); len(closed) != 0 {
		t.Fatalf("the stream closed: %v", closed)
	}
	if got := h.Connections(t, id); len(got) != 1 || got[0].EndReason != "" {
		t.Errorf("machine_connections = %+v, want the first stream still live", got)
	}
}

// Fork 14: the hub's HTTP/2 server answers the agent's keepalive pings and
// never ends a stream for them.
func TestTheRealHubKeepsAStreamThroughShortKeepalivePings(t *testing.T) {
	t.Parallel()
	ka := conn.Keepalive{Ping: 50 * time.Millisecond, Timeout: 200 * time.Millisecond}
	keepaliveSurvives(t, ka, 1500*time.Millisecond)
}

// The default 10 s ping and 20 s timeout in real time (51 s); set FLEET_SLOW_TESTS=1 to run it.
func TestTheRealHubKeepsAStreamThroughDefaultKeepalivePings(t *testing.T) {
	if os.Getenv("FLEET_SLOW_TESTS") == "" {
		t.Skip("real-time keepalive; set FLEET_SLOW_TESTS=1")
	}
	t.Parallel()
	ka := conn.Keepalive{Ping: conn.DefaultKeepalivePing, Timeout: conn.DefaultKeepaliveTimeout}
	keepaliveSurvives(t, ka, 3*ka.Ping+ka.Timeout+time.Second)
}
