package gateway_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The replay rule through the real listener (02i): after a Snapshot of
// seq 10, replayed events at or below 10 are stored as history only, one
// above it as live; a live SHED event gets no row but moves the seq. Each
// is counted by kind and result, and the replay phase is logged once.
func TestEventsAreHistoryAtOrBelowTheSnapshotFloorAndLiveAbove(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	s.send(t, eventMsg(8, true))
	s.send(t, eventMsg(10, true))
	s.send(t, eventMsg(12, false))
	s.send(t, shedMsg(13))
	h.waitSeq(t, m.id, 13)
	rows := h.eventRows(t, m.id)
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (no row for the SHED event): %+v", len(rows), rows)
	}
	for i, want := range []struct {
		seq               int64
		replayed, history bool
	}{{8, true, true}, {10, true, true}, {12, false, false}} {
		r := rows[i]
		if r.seq.Int64 != want.seq || r.replayed != want.replayed || r.history != want.history || r.kind != "pane_exited" ||
			r.delivery != "never_drop" || r.gen != 1 || decode(t, r.body).GetSeq() != uint64(want.seq) { //nolint:gosec // G115: test seqs
			t.Errorf("row %d = %+v, want seq %d replayed %v history %v", i, r, want.seq, want.replayed, want.history)
		}
	}
	h.waitMetric(t, "fleet_hub_events_total", 2, `kind="pane_exited"`, `result="history"`)
	h.waitMetric(t, "fleet_hub_events_total", 1, `kind="pane_exited"`, `result="applied"`)
	h.waitMetric(t, "fleet_hub_events_total", 1, `kind="resource_sample"`, `result="applied"`)
	if lines := h.waitEvents(t, "replay.done", 1); lines[0]["events"] != float64(2) || lines[0]["first_seq"] != float64(8) ||
		lines[0]["last_seq"] != float64(10) {
		t.Errorf("replay.done %v, want 2 events from 8 to 10", lines[0])
	}
}

// 03n "Ordering and consistency": an Event before the stream's Snapshot,
// or one whose seq does not rise on the stream, is dropped and the hub
// asks for a snapshot (once here: both within the 5-minute limit); the refusals are counted, each
// reason logged once.
func TestAnEventOutOfOrderIsDroppedAndAsksForOneSnapshot(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, eventMsg(5, false))
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "out_of_order" {
		t.Fatalf("got %v, want SnapshotRequest{out_of_order}", r)
	}
	s.send(t, eventMsg(6, false))
	s.send(t, snapshotMsg(10, "laptop", "h1"))
	h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	s.send(t, eventMsg(20, false))
	s.send(t, eventMsg(15, false)) // below 20 on this stream
	h.waitMetric(t, "fleet_hub_events_total", 3, `result="refused"`)
	h.waitSeq(t, m.id, 20)
	s.silent(t, 100*time.Millisecond) // no second SnapshotRequest
	got := map[string]int{}
	for _, l := range h.events(t, "event.refused") {
		r, _ := l["reason"].(string)
		got[r]++
	}
	if got["before_snapshot"] != 1 || got["seq_order"] != 1 {
		t.Errorf("event.refused reasons %v, want before_snapshot and seq_order once each", got)
	}
}

// An event the hub already holds (a re-send on a new stream after the old
// one died before the agent saw an ack) is known: no row, no seq move.
func TestAReSentEventIsKnown(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	s.send(t, eventMsg(11, false))
	h.waitSeq(t, m.id, 11)
	h.clock.set(h.clock.now().Add(6 * time.Second)) // past the Hello rate limit
	again := h.open(t, m)
	hi := hello(m, 1)
	hi.GetHello().LastEventSeq = 11 // an agent that kept its state: no rollback
	again.send(t, hi)
	again.ack(t)
	again.send(t, snapshotMsg(12, "laptop", "h1"))
	h.waitRow(t, m.id, "the second snapshot", func(r machineRow) bool { return r.snapEpoch == 2 })
	again.send(t, eventMsg(11, true))
	h.waitMetric(t, "fleet_hub_events_total", 1, `result="known"`)
	if rows := h.eventRows(t, m.id); len(rows) != 1 {
		t.Errorf("%d rows, want 1", len(rows))
	}
}

// The agent's free text in an Event (ProbeHealth.message) is cut to its
// cap and scrubbed before the store; the stored body holds no secret.
func TestEventFreeTextIsScrubbedBeforeTheStore(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	secret := "ghp_" + strings.Repeat("A", 36)
	s.send(t, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: 11, Replayed: true, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE,
		Body: &agentv1.Event_ProbeHealth{ProbeHealth: &agentv1.ProbeHealth{
			Message: "probe failed token " + secret + "\x07" + strings.Repeat("x", 4096),
		}},
	}}})
	h.waitSeq(t, m.id, 11)
	rows := h.eventRows(t, m.id)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	msg := decode(t, rows[0].body).GetProbeHealth().GetMessage()
	if len(msg) > 256 || strings.Contains(msg, secret) || strings.Contains(msg, "\x07") || !strings.HasPrefix(msg, "probe failed") {
		t.Errorf("stored message %q (%d bytes), want it cut to 256, scrubbed, no control byte", msg[:min(len(msg), 60)], len(msg))
	}
	h.waitMetric(t, "fleet_hub_event_text_cleaned_total", 2, `kind="probe_health"`)
}

// An applier failure rolls its event back and ends the stream; the events
// queued behind it fail the store's chain check, so hub_last_event_seq
// stays below the lost event and the agent sends it again.
func TestAFailingEventApplierEndsTheStreamAndLosesNothing(t *testing.T) {
	t.Parallel()
	failing := eventApplierFunc(func(_ context.Context, _ *sql.Tx, _ string, _ statedb.EventOut, ev *agentv1.Event) error {
		if ev.GetSeq() == 12 {
			return errors.New("a row 7 bug")
		}
		return nil
	})
	h := startHub(t, func(o *gateway.ServerOptions) { o.EventAppliers = []ingest.EventApplier{failing} })
	m, s := h.snapshotted(t, 10)
	for seq := uint64(11); seq <= 15; seq++ {
		s.send(t, eventMsg(seq, false))
	}
	if err := s.ended(t); err != nil {
		t.Logf("stream ended: %v", err)
	}
	if seq, _ := h.lastEventSeq(t, m.id); seq != 11 {
		t.Errorf("hub_last_event_seq %d, want 11 (12 failed)", seq)
	}
	if lines := h.waitEvents(t, "event.failed", 1); lines[0]["seq"] != float64(12) || lines[0]["step"] != "applier" {
		t.Errorf("event.failed %v, want seq 12 at the applier", lines[0])
	}
	if n := len(h.events(t, "event.failed")); n != 1 {
		t.Errorf("%d event.failed lines, want 1", n)
	}
}
