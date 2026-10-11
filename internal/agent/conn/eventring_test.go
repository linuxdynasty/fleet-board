package conn_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
)

func newEmitter(t *testing.T, a *agent) *events.Emitter {
	t.Helper()
	e, err := events.New(context.Background(), events.Options{DB: a.openDB(t), Telemetry: a.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func policyEvent(hash string) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_PolicyChanged{PolicyChanged: &agentv1.PolicyChanged{PreviousHash: hash}}}
}

// streamEvents is what stream n received: the index of each message kind
// and the events in order.
func streamEvents(h *connectHub, n int) (snapAt, resultAt int, evs []*agentv1.Event, evAt []int) {
	snapAt, resultAt = -1, -1
	for i, m := range h.stream(n).msgs {
		switch {
		case m.GetSnapshot() != nil && snapAt < 0:
			snapAt = i
		case m.GetCommandResult() != nil:
			resultAt = i
		case m.GetEvent() != nil:
			evs = append(evs, m.GetEvent())
			evAt = append(evAt, i)
		}
	}
	return snapAt, resultAt, evs, evAt
}

// 03n "Ordering" and the 7a proof: the snapshot, then the unacked results,
// then the ring's events in seq order (replayed), then live ones emitted at
// about 200 a second while connected, never out of seq order.
func TestEventsGoOutAfterTheSnapshotAndResultsInSeqOrder(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	logResult(t, db, "cmd-1", time.Now())
	e := newEmitter(t, a)
	var before []uint64
	for i := range 3 {
		s, err := e.Emit(context.Background(), policyEvent(string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		before = append(before, s)
	}
	run := startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
	h.connected(t, 1)
	run.connected(t)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			time.Sleep(time.Duration(i) * 5 * time.Millisecond) // ~200 a second
			if _, err := e.Emit(context.Background(), policyEvent("live")); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	waitFor(t, 10*time.Second, "43 events on stream 1", func() bool {
		_, _, evs, _ := streamEvents(h, 1)
		return len(evs) == 43
	})
	snapAt, resultAt, evs, evAt := streamEvents(h, 1)
	if snapAt < 0 || resultAt < snapAt || evAt[0] < resultAt {
		t.Fatalf("snapshot at %d, result at %d, first event at %d", snapAt, resultAt, evAt[0])
	}
	snapSeq := h.snapshots(1)[0].GetSeq()
	var sent []uint64
	for i, ev := range evs {
		sent = append(sent, ev.GetSeq())
		if want := i < 3; ev.GetReplayed() != want || (ev.GetSeq() < snapSeq) != want {
			t.Errorf("event %d (seq %d, snapshot %d) replayed %v", i, ev.GetSeq(), snapSeq, ev.GetReplayed())
		}
	}
	if !slices.IsSorted(sent) || !slices.Equal(sent[:3], before) || len(slices.Compact(slices.Clone(sent))) != 43 {
		t.Errorf("seqs on the wire %v", sent)
	}
}

// Fork 1 (c): the ring is trimmed at an EventAck only when the hub listed
// event.ack; otherwise the ack is ignored and the entries stay until the
// next HelloAck floor, which drops them on the next stream.
func TestEventAckTrimsTheRingOnlyWhenTheHubListsIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		caps  []*typev1.Capability
		after uint64
	}{
		{"without event.ack", nil, 2},
		{"with event.ack", []*typev1.Capability{{Name: "event.ack", Version: 1}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHubWith(t, func(h *connectHub) {
				h.tamperAck = func(_ int, ack *agentv1.HelloAck) {
					ack.HubCapabilities = tc.caps
					ack.HubLastEventSeq = 0
				}
			})
			a := enrolled(t, h)
			e := newEmitter(t, a)
			var last uint64
			for range 2 {
				s, err := e.Emit(context.Background(), policyEvent("x"))
				if err != nil {
					t.Fatal(err)
				}
				last = s
			}
			startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
			h.connected(t, 1)
			waitFor(t, 10*time.Second, "both events", func() bool { _, _, evs, _ := streamEvents(h, 1); return len(evs) == 2 })
			h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_EventAck{EventAck: &agentv1.EventAck{LastEventSeq: last}}})
			h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
			waitFor(t, 10*time.Second, "the second snapshot", func() bool { return len(h.snapshots(1)) == 2 })
			if got := e.Size().Events; got != tc.after {
				t.Errorf("the ring holds %d after the ack, want %d", got, tc.after)
			}
			ignored := 0
			for _, l := range a.events(t, "connect.message_ignored") {
				if l["kind"] == "event_ack" {
					ignored++
				}
			}
			if want := map[bool]int{true: 1, false: 0}[tc.caps == nil]; ignored != want {
				t.Errorf("connect.message_ignored{kind=event_ack} %d times, want %d", ignored, want)
			}
		})
	}
}

// AgentHealth.ring_events and ring_bytes come from the ring (they were 0
// until row 7 held one).
func TestTheHeartbeatCarriesTheRingSize(t *testing.T) {
	t.Parallel()
	h := startConnectHubWith(t, func(h *connectHub) {
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HeartbeatInterval = durationpb.New(time.Second) }
	})
	a := enrolled(t, h)
	e := newEmitter(t, a)
	if _, err := e.Emit(context.Background(), policyEvent("x")); err != nil {
		t.Fatal(err)
	}
	startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
	h.connected(t, 1)
	waitFor(t, 10*time.Second, "a heartbeat", func() bool { return len(h.heartbeats(1)) > 0 })
	hb := h.heartbeats(1)[0].hb
	if hb.GetHealth().GetRingEvents() != 1 || hb.GetHealth().GetRingBytes() != e.Size().Bytes || hb.GetHealth().GetRingBytes() == 0 {
		t.Errorf("health %v, want 1 event and %d bytes", hb.GetHealth(), e.Size().Bytes)
	}
}

// The 7a proof "replay order after a snapshot": a ring holding more than
// one page (64 a turn) replays whole, after the snapshot, in seq order,
// every event replayed, through serve's own turns.
func TestALongRingReplaysWholeInSeqOrder(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	e := newEmitter(t, a)
	var want []uint64
	for range 150 {
		s, err := e.Emit(context.Background(), policyEvent("x"))
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, s)
	}
	startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
	h.connected(t, 1)
	waitFor(t, 20*time.Second, "150 replayed events", func() bool { _, _, evs, _ := streamEvents(h, 1); return len(evs) >= 150 })
	snapAt, _, evs, evAt := streamEvents(h, 1)
	var got []uint64
	for _, ev := range evs {
		got = append(got, ev.GetSeq())
		if !ev.GetReplayed() {
			t.Errorf("seq %d not marked replayed", ev.GetSeq())
		}
	}
	if !slices.Equal(got, want) || evAt[0] < snapAt {
		t.Errorf("replayed %d events (first at %d, snapshot at %d), in order %v", len(got), evAt[0], snapAt, slices.Equal(got, want))
	}
}
