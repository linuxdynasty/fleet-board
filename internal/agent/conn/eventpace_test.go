package conn_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// fillRing emits n NEVER_DROP events from a few goroutines, so the store
// batches their commits.
func fillRing(t *testing.T, a *agent, n int) {
	t.Helper()
	e := a.emitter(t)
	var wg sync.WaitGroup
	for w := range 20 {
		wg.Go(func() {
			for i := w; i < n; i += 20 {
				if _, err := e.Emit(context.Background(), policyEvent("x")); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// Ruling round 3, 3c (defaults "Agent event send rate"; 04 Q7): the
// stream's event sender keeps to 200 events a second with bursts of 64, so
// a 2,000-entry ring replays in about 10 s, not at line rate: the first 64
// at once, then 1,936 at 200/s, 9.7 s from the first event to the last.
func TestTheEventSenderKeepsTo200ASecond(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	fillRing(t, a, 2000)
	startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = a.emitter(t) })
	h.connected(t, 1)
	waitFor(t, 30*time.Second, "2,000 events", func() bool { _, _, evs, _ := streamEvents(h, 1); return len(evs) == 2000 })
	s := h.stream(1)
	var first, last time.Time
	for i, m := range s.msgs {
		if m.GetEvent() == nil {
			continue
		}
		if first.IsZero() {
			first = s.times[i]
		}
		last = s.times[i]
	}
	if d := last.Sub(first); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("2,000 events took %v from the first to the last, want about 9.7 s (200/s after a burst of 64)", d)
	}
}

// Ruling round 3, 3d on the real stream: a stored entry above the message
// cap is skipped and counted, the stream stays up and the events after it
// still reach the hub.
func TestAnOverCapRingEntryNeverReachesTheStream(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	e := a.emitter(t)
	seq, err := e.Seqs().Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&agentv1.Event{Seq: seq, Body: &agentv1.Event_PolicyChanged{
		PolicyChanged: &agentv1.PolicyChanged{PreviousHash: strings.Repeat("h", protoversion.MaxPartBytes)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := a.openDB(t).SubmitEvent(context.Background(), ring.EventRow{Seq: seq, Created: time.Now(), Body: body}, ring.DefaultCaps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
	good, err := e.Emit(context.Background(), policyEvent("ok"))
	if err != nil {
		t.Fatal(err)
	}
	run := startLoop(t, a, func(o *conn.LoopOptions) { o.Emitter = e })
	h.connected(t, 1)
	waitFor(t, 10*time.Second, "the good event", func() bool { _, _, evs, _ := streamEvents(h, 1); return len(evs) == 1 })
	if _, _, evs, _ := streamEvents(h, 1); evs[0].GetSeq() != good {
		t.Errorf("sent seq %d, want %d", evs[0].GetSeq(), good)
	}
	st, err := a.openDB(t).State(context.Background())
	if err != nil || st.Marks.Evicted != 1 {
		t.Errorf("evicted_total %d (%v), want 1", st.Marks.Evicted, err)
	}
	run.running(t)
	if h.count() != 1 {
		t.Errorf("%d streams, want the first one still up", h.count())
	}
}
