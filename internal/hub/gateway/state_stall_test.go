package gateway_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// stalledHub is a hub whose event arm blocks until release closes or its
// stream's context ends, so the agent can fill the ingest queue and stall
// the stream's reader.
func stalledHub(t *testing.T) (*hub, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetArmContext(o, "event", func(ctx context.Context) {
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
	})
	return h, release
}

// stall sends messages until the stream's reader blocks handing one to the
// full ingest queue (one in the arm, 64 queued, one in the reader).
func (h *hub) stall(t *testing.T, s *agentStream, machineID string) {
	t.Helper()
	for range 66 {
		s.send(t, eventMsg())
	}
	end := time.Now().Add(10 * time.Second)
	for !gateway.Stalled(h.srv, machineID) {
		if time.Now().After(end) {
			t.Fatal("the reader did not stall")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Row 5 ruling, fork 4: at STALE the hub cuts a stream only when its
// reader has been stalled handing a message to the ingest queue for 45 s
// or more; a stall younger than that is left alone. The cut is a transport
// close (no Bye), logged and counted as ingest_stalled; the live row's
// close then takes the machine OFFLINE.
func TestAStalledReaderIsCutAtStaleOnlyAfter45s(t *testing.T) {
	t.Parallel()
	h, _ := stalledHub(t)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	h.clock.set(h.clock.now().Add(30 * time.Second))
	h.stall(t, s, m.id) // stalled from Hello + 30 s
	h.housekeep(t, 20*time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Fatalf("state %s 50 s after the Hello, want STALE", st)
	}
	if n := len(h.events(t, "hub.stream_closed")); n != 0 {
		t.Fatalf("%d streams cut after a 20 s stall, want 0", n)
	}
	h.housekeep(t, 25*time.Second) // stalled 45 s
	if err := s.ended(t); err != nil {
		t.Errorf("the cut stream ended with %v, want a plain close", err)
	}
	h.waitState(t, m.id, "OFFLINE")
	if got := h.conns(t, m.id); len(got) != 1 || got[0].endReason != "ingest_stalled" {
		t.Errorf("rows %+v, want epoch 1 closed as ingest_stalled", got)
	}
	closed := h.events(t, "hub.stream_closed")
	if len(closed) != 1 || closed[0]["reason"] != "ingest_stalled" || closed[0]["stalled_s"] != float64(45) ||
		closed[0]["queue_depth"] != float64(64) || closed[0]["machine_id"] != m.id {
		t.Errorf("hub.stream_closed lines %v", closed)
	}
	h.waitMetric(t, "fleet_hub_stream_closed_total", 1, `reason="ingest_stalled"`)
	if c := h.waitChanges(t, m.id, 2); len(c) != 2 || c[1]["from"] != "STALE" || c[1]["to"] != "OFFLINE" || c[1]["reason"] != "ingest_stalled" {
		t.Errorf("machine.state_changed lines %v", c)
	}
}

// A stall alone never cuts an ONLINE machine's stream: the cut waits for
// STALE (a reader stalls briefly behind a busy writer all the time).
func TestAStalledReaderOnAnOnlineMachineIsLeftAlone(t *testing.T) {
	t.Parallel()
	h, release := stalledHub(t)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	h.stall(t, s, m.id)
	// The reader is blocked, so no Heartbeat can arrive; the hub still
	// waits for 45 s of silence before it marks STALE.
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s, want ONLINE", st)
	}
	close(release)
	s.silent(t, 200*time.Millisecond)
	if n := len(h.events(t, "hub.stream_closed")); n != 0 {
		t.Errorf("%d streams cut, want 0", n)
	}
}
