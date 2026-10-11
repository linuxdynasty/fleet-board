package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// eventAcksUntil reads the hub's messages until the stream ends or quiet
// passes with nothing, and returns the EventAcks in order. At each one it
// checks the ack is not ahead of the hub's committed hub_last_event_seq.
func eventAcksUntil(t *testing.T, h *hub, id string, s *agentStream, quiet time.Duration) []uint64 {
	t.Helper()
	var got []uint64
	for {
		select {
		case m := <-s.msgs:
			if a := m.GetEventAck(); a != nil {
				if committed, _ := h.lastEventSeq(t, id); int64(a.GetLastEventSeq()) > committed { //nolint:gosec // G115: test seqs are small
					t.Errorf("EventAck %d ahead of the committed hub_last_event_seq %d", a.GetLastEventSeq(), committed)
				}
				got = append(got, a.GetLastEventSeq())
			}
		case err := <-s.end:
			s.end <- err
			return got
		case <-time.After(quiet):
			return got
		}
	}
}

// Row 7 ruling fork 1 and round 3, 3e: the hub lists event.ack in its
// HelloAck and sends EventAck{last_event_seq} on the stream once the
// events are committed, never ahead of the commit.
func TestTheHubListsEventAckAndSendsItAfterTheCommit(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	listed := false
	for _, c := range s.ack(t).GetHubCapabilities() {
		listed = listed || (c.GetName() == "event.ack" && c.GetVersion() == 1)
	}
	if !listed {
		t.Error("HelloAck.hub_capabilities lacks event.ack 1")
	}
	s.send(t, snapshotMsg(10, "laptop", "h1"))
	h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	for seq := uint64(11); seq <= 13; seq++ {
		s.send(t, eventMsg(seq, false))
	}
	h.waitSeq(t, m.id, 13)
	got := eventAcksUntil(t, h, m.id, s, 3*time.Second)
	if len(got) == 0 || got[len(got)-1] != 13 {
		t.Fatalf("EventAcks %v, want the last to be 13", got)
	}
	h.waitMetric(t, "fleet_hub_event_acks_total", float64(len(got)), `machine="`+m.id+`"`, `result="sent"`)
}

// With a store that fails an event, no EventAck covers it or anything
// queued behind it: the stream ends and the agent sends them again. The
// first event failing leaves no earlier ack to pace a wrong one behind.
func TestNoEventAckCoversAnEventTheStoreDidNotCommit(t *testing.T) {
	t.Parallel()
	for _, bad := range []uint64{11, 12} {
		t.Run(fmt.Sprintf("seq %d fails", bad), func(t *testing.T) {
			t.Parallel()
			failing := eventApplierFunc(func(_ context.Context, _ *sql.Tx, _ string, _ statedb.EventOut, ev *agentv1.Event) error {
				if ev.GetSeq() == bad {
					return errors.New("the store fails")
				}
				return nil
			})
			h := startHub(t, func(o *gateway.ServerOptions) { o.EventAppliers = []ingest.EventApplier{failing} })
			m, s := h.snapshotted(t, 10)
			for seq := uint64(11); seq <= 15; seq++ {
				s.send(t, eventMsg(seq, false))
			}
			for _, a := range eventAcksUntil(t, h, m.id, s, 5*time.Second) {
				if a >= bad {
					t.Errorf("EventAck %d covers an event the store did not commit (%d failed)", a, bad)
				}
			}
		})
	}
}
