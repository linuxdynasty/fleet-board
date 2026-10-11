package gateway_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// 02i "Gaps": a rise of evicted_total in a Heartbeat is loss: a gap marker,
// the counters stored, fleet_hub_event_gap_total, and
// SnapshotRequest{event_lost} at most once per 5 minutes per machine,
// again after that while it keeps rising. A Heartbeat before the Snapshot
// is not judged. The backlog gauge is Heartbeat.event_seq minus the
// committed seq.
func TestARiseOfEvictedInAHeartbeatAsksForASnapshotAtMostEveryFiveMinutes(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, beatWith(3, 7)) // before the Snapshot: not judged
	s.send(t, snapshotMsg(10, "laptop", "h1"))
	h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	s.send(t, eventMsg(11, false))
	h.waitSeq(t, m.id, 11)
	s.send(t, beatWith(25, 2))
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "event_lost" {
		t.Fatalf("got %v, want SnapshotRequest{event_lost}", r)
	}
	h.waitMetric(t, "fleet_hub_event_gap_total", 1)
	if v := sample(scrape(t, h.fixture), "fleet_hub_replay_backlog_events"); v != 14 {
		t.Errorf("backlog %v, want 25 - 11 = 14", v)
	}
	s.send(t, beatWith(25, 3)) // within 5 minutes: marker, no request
	if l := h.waitEvents(t, "event.lost", 2)[1]; l["snapshot_requested"] != false {
		t.Errorf("second event.lost %v, want no request within 5 minutes", l)
	}
	s.silent(t, 100*time.Millisecond)
	h.clock.set(h.clock.now().Add(gateway.EventLostEvery))
	s.send(t, beatWith(25, 4))
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "event_lost" {
		t.Fatalf("after 5 minutes: %v, want SnapshotRequest{event_lost}", r)
	}
	if _, evicted := h.lastEventSeq(t, m.id); evicted != 4 {
		t.Errorf("stored evicted_total %d, want 4", evicted)
	}
	gaps := 0
	for _, r := range h.eventRows(t, m.id) {
		if r.kind == "gap" {
			gaps++
		}
	}
	lines := h.waitEvents(t, "event.lost", 3)
	if gaps != 3 || lines[0]["evicted_from"] != float64(0) || lines[0]["evicted_to"] != float64(2) ||
		lines[0]["from_seq"] != float64(11) || lines[0]["snapshot_requested"] != true || lines[1]["snapshot_requested"] != false {
		t.Errorf("%d gap rows, event.lost %v", gaps, lines)
	}
}

// A Hello whose evicted_total is above the stored one (ring overflow while
// offline): the Snapshot writes the gap, and the first replayed event logs
// replay.gap with the seq range.
func TestARingOverflowWhileOfflineIsLoggedAtTheFirstReplayedEvent(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	s.send(t, eventMsg(11, false))
	h.waitSeq(t, m.id, 11)
	h.clock.set(h.clock.now().Add(6 * time.Second))
	again := h.open(t, m)
	hi := hello(m, 1)
	hi.GetHello().LastEventSeq = 40
	hi.GetHello().EventCounters = &agentv1.EventCounters{EvictedTotal: 3}
	again.send(t, hi)
	again.ack(t)
	again.send(t, snapshotMsg(40, "laptop", "h1"))
	h.waitRow(t, m.id, "the second snapshot", func(r machineRow) bool { return r.snapEpoch == 2 })
	again.send(t, eventMsg(30, true))
	l := h.waitEvents(t, "replay.gap", 1)[0]
	if l["from_seq"] != float64(11) || l["to_seq"] != float64(30) || l["evicted"] != float64(3) {
		t.Errorf("replay.gap %v, want 11 to 30, 3 evicted", l)
	}
	h.waitMetric(t, "fleet_hub_event_gap_total", 1)
}

// EventAck (row 7 ruling, fork 1) carries only a committed seq, at most
// one per second per stream; the last one held back is sent when its time
// comes.
func TestEventAcksFollowTheCommitAtMostOncePerSecond(t *testing.T) {
	t.Parallel()
	var got acks
	h := startHub(t, func(o *gateway.ServerOptions) { gateway.SetEventAcker(o, got.record) })
	m, s := h.snapshotted(t, 10)
	for seq := uint64(11); seq <= 20; seq++ {
		s.send(t, eventMsg(seq, false))
	}
	h.waitSeq(t, m.id, 20)
	end := time.Now().Add(5 * time.Second)
	for {
		seqs, _ := got.got()
		if len(seqs) > 0 && seqs[len(seqs)-1] == 20 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("acks %v, want the last to be 20", seqs)
		}
		time.Sleep(10 * time.Millisecond)
	}
	seqs, at := got.got()
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] || at[i].Sub(at[i-1]) < 900*time.Millisecond {
			t.Errorf("acks %v at %v: want rising seqs at least a second apart", seqs, at)
		}
	}
}

// The prune job deletes history older than a week by hub time.
func TestThePruneJobDeletesHistoryOlderThanAWeek(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	s.send(t, eventMsg(11, false))
	h.waitSeq(t, m.id, 11)
	if err := h.srv.PruneEvents(context.Background(), h.clock.now().Add(gateway.EventRetention-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(h.eventRows(t, m.id)); n != 1 {
		t.Errorf("%d rows before a week, want 1", n)
	}
	if err := h.srv.PruneEvents(context.Background(), h.clock.now().Add(gateway.EventRetention+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(h.eventRows(t, m.id)); n != 0 {
		t.Errorf("%d rows after a week, want 0", n)
	}
	if l := h.waitEvents(t, "event.pruned", 1)[0]; l["rows"] != float64(1) {
		t.Errorf("event.pruned %v", l)
	}
}
