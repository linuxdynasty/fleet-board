package conn_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// Round 3, 3e: the real hub commits the agent's events, then acks them,
// and the agent's real loop trims its ring; the hub's committed seq is at
// least every seq the agent dropped.
func TestTheRealHubAcksAndTheAgentTrimsItsRing(t *testing.T) {
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
	e := a.emitter(t)
	r := startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
	r.connected(t)
	var last uint64
	for range 3 {
		if last, err = e.Emit(context.Background(), policyEvent("x")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 15*time.Second, "the ring trimmed by the hub's EventAck", func() bool { return e.Size().Events == 0 })
	if m := h.Machine(t, id.MachineID); m.HubLastEventSeq < last {
		t.Errorf("the ring was trimmed to %d but the hub committed only %d", last, m.HubLastEventSeq)
	}
}
