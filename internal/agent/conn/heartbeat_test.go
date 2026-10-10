package conn_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// beat is one heartbeat the hub read, with its place among the stream's
// messages and when it was read.
type beat struct {
	hb  *agentv1.Heartbeat
	i   int
	got time.Time
}

func (h *connectHub) heartbeats(n int) []beat {
	s := h.stream(n)
	var out []beat
	for i, m := range s.msgs {
		if hb := m.GetHeartbeat(); hb != nil {
			out = append(out, beat{hb: hb, i: i, got: s.times[i]})
		}
	}
	return out
}

// pauseReads makes the hub stop reading every stream until the returned
// func is called, so the agent's sends wait on HTTP/2 flow control.
func (h *connectHub) pauseReads() func() {
	g := make(chan struct{})
	h.set(func(h *connectHub) { h.readGate = g })
	return func() {
		h.set(func(h *connectHub) { h.readGate = nil })
		close(g)
	}
}

func heartbeatEvery(d *durationpb.Duration) func(*connectHub) {
	return func(h *connectHub) {
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HeartbeatInterval = d }
	}
}

// A5: the first heartbeat comes one interval after the Snapshot and the
// unacked results, then one per HelloAck.heartbeat_interval, with a rising
// uptime, the highest seq handed out and agent.db's EventCounters. The hook
// fills the rest and cannot change those four.
func TestHeartbeatsFollowTheHubsIntervalAfterTheSnapshot(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	ctx := context.Background()
	db := a.openDB(t)
	if _, err := db.RaiseMarks(ctx, ring.Marks{Shed: 7, Coalesced: 5, Evicted: 2}); err != nil {
		t.Fatal(err)
	}
	logResult(t, db, "cmd-1", time.Now())
	h.set(heartbeatEvery(durationpb.New(time.Second)))
	g := newGate()
	first := true
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(ctx context.Context, _ *agentv1.Snapshot) error {
			if first {
				first = false
				return g.wait(ctx)
			}
			return nil
		}
		o.Heartbeat = func(_ context.Context, hb *agentv1.Heartbeat) {
			hb.Load1 = 0.5
			hb.EventSeq = 999_999
		}
	})
	// The first snapshot is held for 2.5 intervals: nothing comes first.
	waitFor(t, 10*time.Second, "the Hello", func() bool { return h.count() == 1 })
	time.Sleep(2500 * time.Millisecond)
	if msgs := h.stream(1).msgs; len(msgs) != 0 {
		t.Fatalf("before the snapshot the hub got %v", msgs)
	}
	g.open()
	waitFor(t, 15*time.Second, "five heartbeats", func() bool { return len(h.heartbeats(1)) >= 5 })

	s := h.stream(1)
	snap := s.msgs[0].GetSnapshot()
	if snap == nil || s.msgs[1].GetCommandResult().GetCommandId().GetValue() != "cmd-1" {
		t.Fatalf("the stream starts with %v, %v; want the snapshot and then result cmd-1", s.msgs[0], s.msgs[1])
	}
	beats := h.heartbeats(1)
	if beats[0].i != 2 {
		t.Errorf("the first heartbeat is message %d, want 2 (after the snapshot and its result)", beats[0].i)
	}
	if d := beats[0].got.Sub(s.times[1]); d < 500*time.Millisecond || d > 5*time.Second {
		t.Errorf("the first heartbeat came %v after the results, want one interval (1 s; the default is 15 s)", d)
	}
	for k, b := range beats {
		hb := b.hb
		if k > 0 {
			// One late read makes one gap long and the next short, so a
			// single gap is only checked to be in order; the pace is
			// checked over the whole run below.
			if d := b.got.Sub(beats[k-1].got); d <= 0 {
				t.Errorf("heartbeat %d was read %v after the one before", k, d)
			}
			if hb.GetUptime().AsDuration() <= beats[k-1].hb.GetUptime().AsDuration() {
				t.Errorf("uptime %v after %v does not rise", hb.GetUptime().AsDuration(), beats[k-1].hb.GetUptime().AsDuration())
			}
		}
		if st := hb.GetSentTime().AsTime(); hb.GetSentTime() == nil || time.Since(st).Abs() > 10*time.Second {
			t.Errorf("heartbeat %d sent_time %v", k, hb.GetSentTime())
		}
		ev := hb.GetEvents()
		if hb.GetEventSeq() != snap.GetSeq() || ev.GetShedTotal() != 7 || ev.GetCoalescedTotal() != 5 ||
			ev.GetEvictedTotal() != 2 || hb.GetLoad1() != 0.5 {
			t.Errorf("heartbeat %d = %v; want event_seq %d, events 7/5/2 and the hook's load1", k, hb, snap.GetSeq())
		}
	}

	// The pace over four intervals: a late read moves one end by a few
	// hundred ms, which averages out here. Want 1 s each.
	span := beats[len(beats)-1].got.Sub(beats[0].got)
	if per := span / time.Duration(len(beats)-1); per < 800*time.Millisecond || per > 1400*time.Millisecond {
		t.Errorf("%d heartbeats came %v apart on average, want 1 s", len(beats), per)
	}

	// Current values: a later snapshot takes a seq, and a counter rises.
	if _, err := db.RaiseMarks(ctx, ring.Marks{Evicted: 4}); err != nil {
		t.Fatal(err)
	}
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	waitFor(t, 10*time.Second, "a second snapshot", func() bool { return len(h.snapshots(1)) == 2 })
	seq2 := h.snapshots(1)[1].GetSeq()
	n := len(h.heartbeats(1))
	waitFor(t, 10*time.Second, "a heartbeat after it", func() bool { return len(h.heartbeats(1)) > n+1 })
	last := h.heartbeats(1)[len(h.heartbeats(1))-1].hb
	if last.GetEventSeq() != seq2 || seq2 <= snap.GetSeq() || last.GetEvents().GetEvictedTotal() != 4 {
		t.Errorf("a later heartbeat = %v; want event_seq %d and evicted 4", last, seq2)
	}
	if ev := a.events(t, "connect.hello"); len(ev) != 1 || ev[0]["heartbeat_ms"] != float64(1000) {
		t.Errorf("connect.hello = %v, want heartbeat_ms 1000", ev)
	}
}

// The agent follows HelloAck.heartbeat_interval from 1 to 15 s; anything
// else (none, 0, under 1 s, over 15 s, negative) is 15 s.
func TestTheHeartbeatIntervalIsOneTo15sElse15s(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    *durationpb.Duration
		want float64
	}{
		{"none", nil, 15000},
		{"zero", durationpb.New(0), 15000},
		{"half a second", durationpb.New(500 * time.Millisecond), 15000},
		{"one second", durationpb.New(time.Second), 1000},
		{"ten seconds", durationpb.New(10 * time.Second), 10000},
		{"fifteen seconds", durationpb.New(15 * time.Second), 15000},
		{"a minute", durationpb.New(time.Minute), 15000},
		{"negative", durationpb.New(-time.Second), 15000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			h.set(heartbeatEvery(tc.d))
			r := startLoop(t, a, nil)
			h.connected(t, 1)
			r.connected(t)
			if ev := a.events(t, "connect.hello"); len(ev) != 1 || ev[0]["heartbeat_ms"] != tc.want {
				t.Errorf("connect.hello = %v, want heartbeat_ms %v", ev, tc.want)
			}
			if tc.want == 15000 {
				time.Sleep(1500 * time.Millisecond)
				if n := len(h.heartbeats(1)); n != 0 {
					t.Errorf("%d heartbeats within 1.5 s at a 15 s interval", n)
				}
			}
		})
	}
}

// A minute from the hub runs at 15 s in real time; set FLEET_SLOW_TESTS=1.
func TestAMinuteFromTheHubBeatsEvery15sInRealTime(t *testing.T) {
	if os.Getenv("FLEET_SLOW_TESTS") == "" {
		t.Skip("real-time heartbeat; set FLEET_SLOW_TESTS=1")
	}
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(heartbeatEvery(durationpb.New(time.Minute)))
	startLoop(t, a, nil)
	h.connected(t, 1)
	waitFor(t, 20*time.Second, "a heartbeat", func() bool { return len(h.heartbeats(1)) == 1 })
	s := h.stream(1)
	if d := h.heartbeats(1)[0].got.Sub(s.times[0]); d < 14*time.Second || d > 17*time.Second {
		t.Errorf("the first heartbeat came %v after the snapshot, want 15 s", d)
	}
}

// One goroutine sends: while a heartbeat is being built (its hook held),
// a SnapshotRequest waits, and the snapshot goes once the heartbeat is
// done. A heartbeat sent from a goroutine of its own lets the snapshot
// through at once, and fails this test.
func TestASnapshotWaitsForTheHeartbeatBeingBuilt(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(heartbeatEvery(durationpb.New(time.Second)))
	g := newGate()
	entered := make(chan struct{})
	var once sync.Once
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Heartbeat = func(_ context.Context, _ *agentv1.Heartbeat) {
			once.Do(func() {
				close(entered)
				_ = g.wait(context.Background())
			})
		}
	})
	t.Cleanup(g.open)
	h.connected(t, 1)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat was built")
	}
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	time.Sleep(time.Second)
	if n := len(a.events(t, "snapshot.sent")); n != 1 {
		t.Fatalf("%d snapshots were sent while a heartbeat was being built, want 1 (the first)", n)
	}
	g.open()
	waitFor(t, 10*time.Second, "the requested snapshot", func() bool { return len(a.events(t, "snapshot.sent")) == 2 })
	waitFor(t, 10*time.Second, "both on the wire", func() bool { return len(h.snapshots(1)) == 2 })
	held, second := -1, -1
	for i, m := range h.stream(1).msgs {
		switch {
		case m.GetHeartbeat() != nil && held < 0:
			held = i
		case m.GetSnapshot() != nil && i > 0:
			second = i
		}
	}
	if held < 0 || second < held {
		t.Errorf("the stream is %v, want the held heartbeat before the requested snapshot", kinds(h.stream(1).msgs))
	}
	r.running(t)
}

// While a send waits on a hub that stopped reading, every snapshot still
// arrives whole and directly followed by its unacked results once the hub
// reads again.
func TestABlockedSendNeverSplitsASnapshotFromItsResults(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	logResult(t, db, "cmd-1", time.Now().Add(-time.Minute))
	logResult(t, db, "cmd-2", time.Now())
	h.set(heartbeatEvery(durationpb.New(time.Second)))
	// 600 KiB per snapshot: three of them outgrow HTTP/2's 1 MiB window.
	pad := strings.Repeat("x", 600<<10)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(_ context.Context, s *agentv1.Snapshot) error {
			s.Facts.OsVersion = pad
			return nil
		}
	})
	h.connected(t, 1)
	waitFor(t, 10*time.Second, "a heartbeat", func() bool { return len(h.heartbeats(1)) >= 1 })

	resume := h.pauseReads()
	for range 3 {
		h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	}
	time.Sleep(3500 * time.Millisecond)
	if sent := len(a.events(t, "snapshot.sent")); sent >= 4 {
		resume()
		t.Fatalf("all %d snapshots were sent while the hub did not read; the send never waited", sent)
	}
	resume()
	waitFor(t, 20*time.Second, "four snapshots and a heartbeat after them", func() bool {
		snaps, lastSnap, lastBeat := 0, -1, -1
		for i, m := range h.stream(1).msgs {
			switch {
			case m.GetSnapshot() != nil:
				snaps, lastSnap = snaps+1, i
			case m.GetHeartbeat() != nil:
				lastBeat = i
			}
		}
		return snaps == 4 && lastBeat > lastSnap
	})
	r.running(t)
	msgs := h.stream(1).msgs
	for i, m := range msgs {
		if m.GetSnapshot() == nil {
			continue
		}
		if i+2 >= len(msgs) || msgs[i+1].GetCommandResult().GetCommandId().GetValue() != "cmd-1" ||
			msgs[i+2].GetCommandResult().GetCommandId().GetValue() != "cmd-2" {
			t.Errorf("snapshot at %d is not followed by its results: %v", i, kinds(msgs))
		}
		if len(m.GetSnapshot().GetFacts().GetOsVersion()) != len(pad) {
			t.Errorf("snapshot at %d arrived cut", i)
		}
	}
	if h.stream(1).ended {
		t.Errorf("the stream ended: %v", kinds(msgs))
	}
}

// kinds names each message, for a failure message.
func kinds(msgs []*agentv1.ConnectRequest) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.GetSnapshot() != nil:
			out = append(out, "snapshot")
		case m.GetCommandResult() != nil:
			out = append(out, "result:"+m.GetCommandResult().GetCommandId().GetValue())
		case m.GetHeartbeat() != nil:
			out = append(out, "heartbeat")
		default:
			out = append(out, "other")
		}
	}
	return out
}
