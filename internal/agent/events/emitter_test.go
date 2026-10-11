package events_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

func pane2(id string) *typev1.PaneKey {
	return &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: id}
}

// Ruling fork 2 / 7a proof: a NEVER_DROP event is in the ring once Emit
// returns, before anything queues it, so a crash right after (no stream,
// the process gone) still replays it on the next connect, marked replayed.
func TestANeverDropEventSurvivesACrashBeforeItIsSent(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	r := newRig(t, dir, events.Options{})
	seq := emit(t, r.e, policyEv("h1"))
	r.close() // no stream ever attached: the "crash" between ring and queue

	r2 := newRig(t, dir, events.Options{})
	c, err := r2.e.Attach(t.Context(), 0, seq+5)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	got := drain(t, c)
	if len(got) != 1 || got[0].GetSeq() != seq || !got[0].GetReplayed() ||
		got[0].GetPolicyChanged().GetPreviousHash() != "h1" || got[0].GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP {
		t.Fatalf("replayed %v", got)
	}
	if r2.metric(t, "fleet_agent_replayed_events_total") != "1" {
		t.Errorf("replayed_events_total %q, want 1", r2.metric(t, "fleet_agent_replayed_events_total"))
	}
}

// 02i: SHED never touches the ring; one emitted with no stream is dropped
// and counted, and the count reaches agent.db (Hello reads it from there).
func TestAShedEventWithNoStreamIsCountedNotKept(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	r := newRig(t, dir, events.Options{})
	emit(t, r.e, sampleEv())
	emit(t, r.e, sampleEv())
	r.e.Close()
	if m := r.marks(t); m.Shed != 2 {
		t.Errorf("shed_total %d, want 2", m.Shed)
	}
	if s, _ := r.db.EventsSize(t.Context()); s.Events != 0 {
		t.Errorf("the ring holds %d events, want none", s.Events)
	}
	if r.metric(t, "fleet_agent_events_shed_total") != "2" {
		t.Errorf("events_shed_total %q", r.metric(t, "fleet_agent_events_shed_total"))
	}
}

// The send queue sheds the oldest SHED event first, and what is left at a
// stream's end is counted too.
func TestTheShedQueueDropsTheOldestFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{ShedQueue: 3})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	var all []uint64
	for range 5 {
		all = append(all, emit(t, r.e, sampleEv()))
	}
	got, _, err := c.Next(t.Context(), 2)
	if err != nil || !slices.Equal(seqs(got), all[2:4]) {
		t.Fatalf("first page %v (%v), want %v", seqs(got), err, all[2:4])
	}
	c.Detach() // seq all[4] is still queued
	r.e.Close()
	if m := r.marks(t); m.Shed != 3 {
		t.Errorf("shed_total %d, want 3 (two pushed out, one left at the end)", m.Shed)
	}
}

// A newer COALESCE event replaces the queued one with its key; the survivor
// keeps its own seq and the replaced number is a hole.
func TestACoalesceEventReplacesTheQueuedOne(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	emit(t, r.e, inputEv("%1"))
	b := emit(t, r.e, inputEv("%2"))
	a2 := emit(t, r.e, inputEv("%1"))
	got := drain(t, c)
	if !slices.Equal(seqs(got), []uint64{b, a2}) || got[1].GetCoalesceKey() != "input:default/s1/%1" {
		t.Fatalf("sent %v, want [%d %d]", seqs(got), b, a2)
	}
	if m := r.marks(t); m.Coalesced != 1 {
		t.Errorf("coalesced_total %d, want 1", m.Coalesced)
	}
}

// 02i rule 2 and fork 1 (c): at attach the entries at or below the HelloAck
// floor go (the hub holds them); without event.ack nothing else is trimmed.
// An EventAck trims only what this stream sent.
func TestTheFloorAndTheAckTrimTheRing(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	var s []uint64
	for i := range 4 {
		s = append(s, emit(t, r.e, policyEv(string(rune('a'+i)))))
	}
	c, err := r.e.Attach(t.Context(), s[1], s[3])
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	got := drain(t, c)
	if !slices.Equal(seqs(got), s[2:]) || !got[0].GetReplayed() || got[1].GetReplayed() {
		t.Fatalf("after the floor %d: %v (replayed %v %v)", s[1], seqs(got), got[0].GetReplayed(), got[1].GetReplayed())
	}
	if size := r.e.Size(); size.Events != 2 {
		t.Errorf("the ring holds %d, want 2 after the floor", size.Events)
	}
	late := emit(t, r.e, policyEv("late")) // queued, not sent yet
	if err := c.Ack(t.Context(), late+100); err != nil {
		t.Fatal(err)
	}
	if size := r.e.Size(); size.Events != 1 {
		t.Errorf("an ack above what was sent trimmed the unsent entry: %d left", size.Events)
	}
	if got := drain(t, c); !slices.Equal(seqs(got), []uint64{late}) {
		t.Errorf("then sent %v, want [%d]", seqs(got), late)
	}
	if r.metric(t, "fleet_agent_ring_dropped_total", `reason="acked"`) != "2" ||
		r.metric(t, "fleet_agent_ring_dropped_total", `reason="floor"`) != "2" {
		t.Errorf("ring_dropped_total acked %q floor %q", r.metric(t, "fleet_agent_ring_dropped_total", `reason="acked"`),
			r.metric(t, "fleet_agent_ring_dropped_total", `reason="floor"`))
	}
	if len(r.logs.events("ring.trimmed")) != 1 {
		t.Errorf("ring.trimmed logged %d times at info, want 1 (the floor; an ack trim is debug)", len(r.logs.events("ring.trimmed")))
	}
}

// The cap evicts the oldest entry whatever its class: loss, counted and
// logged once per eviction.
func TestAnEvictionIsCountedAndLogged(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{Caps: ring.Caps{MaxBytes: 70, MaxAge: time.Hour}})
	first := emit(t, r.e, policyEv("aaaaaaaaaaaaaaaaaaaa"))
	emit(t, r.e, policyEv("bbbbbbbbbbbbbbbbbbbb"))
	if m := r.marks(t); m.Evicted != 1 {
		t.Fatalf("evicted_total %d, want 1", m.Evicted)
	}
	ls := r.logs.events("ring.evicted")
	if len(ls) != 1 || ls[0]["oldest_seq"] != float64(first) || ls[0]["n"] != float64(1) {
		t.Errorf("ring.evicted lines %v", ls)
	}
	if r.metric(t, "fleet_agent_events_evicted_total") != "1" {
		t.Errorf("events_evicted_total %q", r.metric(t, "fleet_agent_events_evicted_total"))
	}
}

// The wire order is seq order (03n "Ordering"): a SHED event never goes
// ahead of a lower NEVER_DROP one still inside its ring commit.
func TestAShedEventWaitsForALowerEventStillCommitting(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	hold, held := make(chan struct{}), make(chan uint64, 1)
	events.SetAfterSubmit(r.e, func(seq uint64) {
		held <- seq
		<-hold
	})
	var wg sync.WaitGroup
	wg.Go(func() { emit(t, r.e, policyEv("slow")) })
	durable := <-held
	events.SetAfterSubmit(r.e, nil)
	shed := emit(t, r.e, sampleEv())
	if got, _, err := c.Next(t.Context(), 64); err != nil || len(got) != 0 {
		t.Fatalf("sent %v (%v) while seq %d was still committing", seqs(got), err, durable)
	}
	close(hold)
	wg.Wait()
	if got := drain(t, c); !slices.Equal(seqs(got), []uint64{durable, shed}) {
		t.Fatalf("sent %v, want [%d %d]", seqs(got), durable, shed)
	}
}

// 7a proof: 200 producers emit at once while the sender drains; every
// NEVER_DROP event arrives once and the sent seqs only ever rise.
func TestConcurrentProducersArriveInSeqOrder(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{ShedQueue: 1000})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	durable := make(chan uint64, 200)
	var wg sync.WaitGroup
	for i := range 200 {
		wg.Go(func() {
			if i%3 == 0 {
				emit(t, r.e, sampleEv())
				return
			}
			durable <- emit(t, r.e, policyEv("x"))
		})
	}
	var sent []uint64
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-r.e.Ready():
		case <-done:
			sent = append(sent, seqs(drain(t, c))...)
			close(durable)
			want := map[uint64]bool{}
			for s := range durable {
				want[s] = true
			}
			if !slices.IsSorted(sent) || len(slices.Compact(slices.Clone(sent))) != len(sent) {
				t.Fatalf("sent out of order or twice: %v", sent)
			}
			for _, s := range sent {
				delete(want, s)
			}
			if len(want) != 0 {
				t.Fatalf("%d never-drop events never sent", len(want))
			}
			return
		}
		got, _, err := c.Next(t.Context(), 16)
		if err != nil {
			t.Fatal(err)
		}
		sent = append(sent, seqs(got)...)
	}
}

func bigPolicyEv(n int) *agentv1.Event {
	return policyEv(strings.Repeat("h", n))
}

// Ruling round 3, 3d: an event above the 02e cap can never be sent, so Emit
// refuses it (ErrOversize) instead of storing a poison ring entry; a
// refused NEVER_DROP event is loss (evicted_total + 1), a refused SHED one
// counts as shed. The log line comes at most once per kind per minute.
func TestAnOversizeEventIsRefusedAndCounted(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	for range 2 {
		if _, err := r.e.Emit(t.Context(), bigPolicyEv(protoversion.MaxPartBytes)); !errors.Is(err, events.ErrOversize) {
			t.Fatalf("Emit of an over-cap event: %v, want ErrOversize", err)
		}
	}
	big := sampleEv()
	big.GetResourceSample().Disk = []*agentv1.DiskFree{{Path: strings.Repeat("p", protoversion.MaxPartBytes)}}
	if _, err := r.e.Emit(t.Context(), big); !errors.Is(err, events.ErrOversize) {
		t.Fatalf("Emit of an over-cap SHED event: %v, want ErrOversize", err)
	}
	r.e.Close()
	if m := r.marks(t); m.Evicted != 2 || m.Shed != 1 {
		t.Errorf("evicted_total %d, shed_total %d; want 2 and 1", m.Evicted, m.Shed)
	}
	if s, _ := r.db.EventsSize(t.Context()); s.Events != 0 {
		t.Errorf("the ring holds %d entries", s.Events)
	}
	if got := len(r.logs.events("ring.emit_refused")); got != 2 {
		t.Errorf("ring.emit_refused logged %d times, want 2 (once per kind per minute)", got)
	}
	if v := r.metric(t, "fleet_agent_events_refused_total", `reason="oversize"`); v != "3" {
		t.Errorf("events_refused_total %q, want 3", v)
	}
	if _, err := r.e.Emit(t.Context(), bigPolicyEv(100)); !errors.Is(err, events.ErrClosed) {
		t.Errorf("after Close: %v", err)
	}
}

// A stored entry above the cap (written before the guard, or by hand) is
// skipped like an undecodable one, counted as loss, and the events after it
// still go out.
func TestAnOverCapRingEntryIsSkippedAndCounted(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	alloc := r.e.Seqs()
	first, err := alloc.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	body, err := proto.Marshal(&agentv1.Event{Seq: first, Body: &agentv1.Event_PolicyChanged{
		PolicyChanged: &agentv1.PolicyChanged{PreviousHash: strings.Repeat("h", protoversion.MaxPartBytes)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wait, err := r.db.SubmitEvent(t.Context(), ring.EventRow{Seq: first, Created: time.Now(), Body: body}, ring.DefaultCaps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
	after := emit(t, r.e, policyEv("ok"))
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, c); !slices.Equal(seqs(got), []uint64{after}) {
		t.Fatalf("sent %v, want only %d", seqs(got), after)
	}
	c.Detach()
	// The entry is gone with its count: the next stream neither sends nor
	// counts it again.
	c2, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Detach()
	if got := drain(t, c2); !slices.Equal(seqs(got), []uint64{after}) {
		t.Fatalf("the next stream sent %v, want only %d", seqs(got), after)
	}
	if m := r.marks(t); m.Evicted != 1 {
		t.Errorf("evicted_total %d, want 1", m.Evicted)
	}
	if size := r.e.Size(); size.Events != 1 {
		t.Errorf("the ring holds %d entries, want 1 (the over-cap one deleted)", size.Events)
	}
	ls := r.logs.events("ring.unreadable")
	if len(ls) != 1 || ls[0]["err_kind"] != "oversize" {
		t.Errorf("ring.unreadable lines %v", ls)
	}
}
