package gateway_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// Arbiter ruling (round 2, B): an out-of-order event is dropped and
// counted, the stream stays open and later in-order events apply, and the
// ask for a snapshot shares the per-machine 5-minute limit with event_lost:
// two disorder bursts 6 minutes apart give two requests, two within 5
// minutes give one.
func TestOutOfOrderEventsAskForASnapshotUnderTheEventLostLimit(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	s.send(t, eventMsg(20, false))
	h.waitSeq(t, m.id, 20)
	s.send(t, eventMsg(15, false)) // at or below 20 on this stream
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "out_of_order" {
		t.Fatalf("got %v, want SnapshotRequest{out_of_order}", r)
	}
	h.clock.set(h.clock.now().Add(4 * time.Minute))
	s.send(t, eventMsg(16, false)) // within 5 minutes: dropped, no ask
	h.waitMetric(t, "fleet_hub_events_total", 2, `result="refused"`)
	s.silent(t, 100*time.Millisecond)
	s.send(t, eventMsg(21, false)) // the stream stays open
	h.waitSeq(t, m.id, 21)
	h.clock.set(h.clock.now().Add(2 * time.Minute)) // 6 minutes after the first ask
	s.send(t, eventMsg(17, false))
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "out_of_order" {
		t.Fatalf("6 minutes later: %v, want a second SnapshotRequest{out_of_order}", r)
	}
	s.send(t, eventMsg(22, false))
	h.waitSeq(t, m.id, 22)
}

// The two reasons share one limit per machine: an event_lost ask inside
// the window suppresses an out_of_order one, and the reverse.
func TestEventLostAndOutOfOrderShareOneLimit(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"event_lost", "out_of_order"} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			h := startHub(t)
			m, s := h.snapshotted(t, 10)
			s.send(t, eventMsg(20, false))
			h.waitSeq(t, m.id, 20)
			lost := func() { s.send(t, beatWith(20, 1)) }
			disorder := func() { s.send(t, eventMsg(15, false)) }
			do, then := lost, disorder
			if first == "out_of_order" {
				do, then = disorder, lost
			}
			do()
			if r := s.next(t).GetSnapshotRequest(); r.GetReason() != first {
				t.Fatalf("got %v, want SnapshotRequest{%s}", r, first)
			}
			h.clock.set(h.clock.now().Add(gateway.EventLostEvery - time.Second))
			then()
			if first == "out_of_order" {
				h.waitEvents(t, "event.lost", 1)
			} else {
				h.waitMetric(t, "fleet_hub_events_total", 1, `result="refused"`)
			}
			s.silent(t, 100*time.Millisecond)
		})
	}
}
