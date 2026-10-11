package events_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// storeRow takes the next seq and writes body as its ring entry directly,
// as a file written before a guard, or damaged, would hold it.
func storeRow(t *testing.T, r *rig, body func(seq uint64) []byte) uint64 {
	t.Helper()
	seq, err := r.e.Seqs().Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wait, err := r.db.SubmitEvent(t.Context(), ring.EventRow{Seq: seq, Created: time.Now(), Body: body(seq)}, ring.DefaultCaps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
	return seq
}

func mismatched(seq uint64) []byte {
	b, _ := proto.Marshal(&agentv1.Event{Seq: seq + 1000, Body: &agentv1.Event_PolicyChanged{PolicyChanged: &agentv1.PolicyChanged{}}})
	return b
}

func overCap(seq uint64) []byte {
	b, _ := proto.Marshal(&agentv1.Event{Seq: seq, Body: &agentv1.Event_PolicyChanged{
		PolicyChanged: &agentv1.PolicyChanged{PreviousHash: strings.Repeat("h", protoversion.MaxPartBytes)},
	}})
	return b
}

// pages calls Next(limit) until it has nothing more and returns every seq.
func pages(t *testing.T, c *events.Cursor, limit int) []uint64 {
	t.Helper()
	var out []uint64
	for range 20 {
		got, more, err := c.Next(t.Context(), limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > limit {
			t.Fatalf("Next(%d) returned %d events", limit, len(got))
		}
		out = append(out, seqs(got)...)
		if !more && len(got) == 0 {
			return out
		}
	}
	t.Fatal("the cursor never ran dry")
	return nil
}

// Review P1 (two probes): a full page with a skipped row must not let a
// SHED event above the rows read go first, nor move the cursor past ring
// rows not read yet; the skip of a bad row is counted once.
func TestASkippedRowInAFullPageLosesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		bad     func(uint64) []byte
		evicted uint64
	}{
		{"undecodable row", mismatched, 1},
		{"over-cap row", overCap, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, stateDir(t), events.Options{})
			c, err := r.e.Attach(t.Context(), 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Detach()
			s1, s2 := emit(t, r.e, sampleEv()), emit(t, r.e, sampleEv())
			s3 := emit(t, r.e, policyEv("3"))
			storeRow(t, r, tc.bad)
			s5 := emit(t, r.e, policyEv("5"))
			if got := pages(t, c, 2); !slices.Equal(got, []uint64{s1, s2, s3, s5}) {
				t.Errorf("sent %v, want %v", got, []uint64{s1, s2, s3, s5})
			}
			if m := r.marks(t); m.Evicted != tc.evicted {
				t.Errorf("evicted_total %d, want %d", m.Evicted, tc.evicted)
			}
		})
	}
}

// The second probe: an over-cap row first, then two NEVER_DROP and a SHED.
func TestAnOverCapFirstRowDoesNotLetShedJumpAhead(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	storeRow(t, r, overCap)
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	s2, s3 := emit(t, r.e, policyEv("2")), emit(t, r.e, policyEv("3"))
	s4 := emit(t, r.e, sampleEv())
	if got := pages(t, c, 2); !slices.Equal(got, []uint64{s2, s3, s4}) {
		t.Errorf("sent %v, want %v", got, []uint64{s2, s3, s4})
	}
	if m := r.marks(t); m.Evicted != 1 {
		t.Errorf("evicted_total %d, want 1", m.Evicted)
	}
}

// Every page size from 1 to 6 over the same mixed ring sends the same
// events in the same order, and an EventAck at the last sent seq after
// each page never trims an entry not sent yet.
func TestEveryPageSizeSendsTheSameOrder(t *testing.T) {
	t.Parallel()
	for limit := 1; limit <= 6; limit++ {
		r := newRig(t, stateDir(t), events.Options{})
		bad := storeRow(t, r, mismatched)
		c, err := r.e.Attach(t.Context(), 0, 1)
		if err != nil {
			t.Fatal(err)
		}
		var want []uint64
		for i := range 4 {
			want = append(want, emit(t, r.e, sampleEv()))
			want = append(want, emit(t, r.e, policyEv(string(rune('a'+i)))))
		}
		storeRow(t, r, overCap)
		want = append(want, emit(t, r.e, policyEv("z")))
		var got []uint64
		for range 40 {
			evs, more, err := c.Next(t.Context(), limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) > limit {
				t.Fatalf("Next(%d) returned %d events", limit, len(evs))
			}
			got = append(got, seqs(evs)...)
			if len(got) > 0 {
				if err := c.Ack(t.Context(), got[len(got)-1]); err != nil {
					t.Fatal(err)
				}
			}
			if !more && len(evs) == 0 {
				break
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("limit %d: sent %v, want %v (bad row %d)", limit, got, want, bad)
		}
		if m := r.marks(t); m.Evicted != 2 {
			t.Errorf("limit %d: evicted_total %d, want 2 (the two bad rows, once each)", limit, m.Evicted)
		}
		c.Detach()
	}
}

// Review P2: a durable event whose ring write fails has taken a seq and is
// in no ring: it is counted as loss and Emit says so.
func TestAFailedRingWriteIsCountedAsLoss(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	// An entry far above the allocator: the file refuses the next, lower one.
	wait, err := r.db.SubmitEvent(t.Context(), ring.EventRow{Seq: 1 << 40, Created: time.Now(), Body: []byte("x")}, ring.DefaultCaps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.e.Emit(t.Context(), policyEv("lost")); err == nil {
		t.Fatal("Emit saved an event the file refused")
	}
	if m := r.marks(t); m.Evicted != 1 {
		t.Errorf("evicted_total %d, want 1", m.Evicted)
	}
}

// Review P2: ring.emit_failed names a real cause, never a blanket "io".
func TestEmitFailedNamesTheCause(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	r.e.Close()
	events.PolicyChanges(r.e)(policy.Change{Hash: "h", Time: time.Now()})
	ls := r.logs.events("ring.emit_failed")
	if len(ls) != 1 || ls[0]["err_kind"] != "closed" || ls[0]["kind"] != "policy_changed" {
		t.Errorf("ring.emit_failed lines %v", ls)
	}
}

// Review P2: a Detach from a stream that a newer Attach replaced changes
// nothing: the new stream keeps its SHED queue.
func TestAStaleDetachDoesNotDetachTheNewStream(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	old, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Detach()
	old.Detach()
	s := emit(t, r.e, sampleEv())
	if got := drain(t, cur); !slices.Equal(seqs(got), []uint64{s}) {
		t.Errorf("the new stream got %v, want [%d]", seqs(got), s)
	}
	if m := r.marks(t); m.Shed != 0 {
		t.Errorf("shed_total %d: the stale Detach dropped the queue", m.Shed)
	}
}

// Review F1: a cursor a newer Attach replaced takes nothing: its Next
// returns nothing and the new stream keeps the SHED event.
func TestAStaleCursorTakesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	old, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer cur.Detach()
	s := emit(t, r.e, sampleEv())
	if got, more, err := old.Next(t.Context(), 8); err != nil || more || len(got) != 0 {
		t.Fatalf("the stale cursor got %v (more %v, %v), want nothing", seqs(got), more, err)
	}
	if got := drain(t, cur); !slices.Equal(seqs(got), []uint64{s}) {
		t.Errorf("the new stream got %v, want [%d]", seqs(got), s)
	}
}

// Review F2: a second Attach counts the SHED events it drops from the
// earlier stream's queue.
func TestASecondAttachCountsTheShedQueueItDrops(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	if _, err := r.e.Attach(t.Context(), 0, 1); err != nil {
		t.Fatal(err)
	}
	emit(t, r.e, sampleEv())
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Detach()
	r.e.Close()
	if m := r.marks(t); m.Shed != 1 {
		t.Errorf("shed_total %d, want 1", m.Shed)
	}
}

// Review P3: a limit below 1 returns nothing at once.
func TestALimitBelowOneReturnsNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, stateDir(t), events.Options{})
	c, err := r.e.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	emit(t, r.e, policyEv("x"))
	emit(t, r.e, sampleEv())
	for _, limit := range []int{0, -1} {
		done := make(chan struct{})
		var got []*agentv1.Event
		var more bool
		go func() { got, more, err = c.Next(t.Context(), limit); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("Next(%d) did not return", limit)
		}
		if err != nil || more || len(got) != 0 {
			t.Errorf("Next(%d) = %v, %v, %v; want nothing", limit, seqs(got), more, err)
		}
	}
	if got := drain(t, c); len(got) != 2 {
		t.Errorf("after the empty calls the stream got %d events, want 2", len(got))
	}
}
