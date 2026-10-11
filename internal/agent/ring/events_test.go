package ring_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

var ringT0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// bigCaps never evicts.
var bigCaps = ring.Caps{MaxBytes: 1 << 30, MaxAge: 365 * 24 * time.Hour}

func put(t *testing.T, db *ring.DB, e ring.EventRow, caps ring.Caps) ring.Appended {
	t.Helper()
	wait, err := db.SubmitEvent(context.Background(), e, caps)
	if err != nil {
		t.Fatalf("SubmitEvent %d: %v", e.Seq, err)
	}
	got, err := wait()
	if err != nil {
		t.Fatalf("SubmitEvent %d committed with %v", e.Seq, err)
	}
	return got
}

func ev(seq uint64, key string, n int) ring.EventRow {
	return ring.EventRow{Seq: seq, Key: key, Created: ringT0, Body: []byte(strings.Repeat("x", n))}
}

func between(t *testing.T, db *ring.DB, after, before uint64, limit int) []uint64 {
	t.Helper()
	es, err := db.EventsBetween(context.Background(), after, before, limit)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]uint64, 0, len(es))
	for _, e := range es {
		out = append(out, e.Seq)
	}
	return out
}

// 02i: the ring keeps NEVER_DROP and COALESCE events in seq order, and they
// survive a reopen (03n W2 "Persistence": per event, before it is queued).
func TestTheRingKeepsEventsInSeqOrderAcrossAReopen(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	for _, s := range []uint64{3, 5, 9} {
		put(t, db, ev(s, "", 10), bigCaps)
	}
	got := put(t, db, ev(12, "ready:p", 7), bigCaps)
	if got.Size != (ring.Size{Events: 4, Bytes: 37}) {
		t.Errorf("size %+v, want 4 events and 37 bytes", got.Size)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	if s := between(t, db, 0, ^uint64(0), 100); !slices.Equal(s, []uint64{3, 5, 9, 12}) {
		t.Errorf("after a reopen the ring holds %v", s)
	}
	if s := between(t, db, 3, 12, 100); !slices.Equal(s, []uint64{5, 9}) {
		t.Errorf("between 3 and 12: %v, want [5 9]", s)
	}
	if s := between(t, db, 0, ^uint64(0), 2); !slices.Equal(s, []uint64{3, 5}) {
		t.Errorf("limit 2: %v", s)
	}
	es, err := db.EventsBetween(context.Background(), 11, 13, 1)
	if err != nil || len(es) != 1 || es[0].Key != "ready:p" || string(es[0].Body) != "xxxxxxx" || !es[0].Created.Equal(ringT0) {
		t.Errorf("entry 12 read back as %+v, %v", es, err)
	}
	size, err := db.EventsSize(context.Background())
	if err != nil || size != (ring.Size{Events: 4, Bytes: 37}) {
		t.Errorf("RingSize after a reopen: %+v, %v", size, err)
	}
}

// 02i "Queue and ring rules": one COALESCE entry per key, replaced by the
// newer event, which keeps its own seq; coalesced_total counts it.
func TestACoalesceEventReplacesTheOneWithItsKey(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	put(t, db, ev(1, "input:a", 5), bigCaps)
	put(t, db, ev(2, "", 5), bigCaps)
	put(t, db, ev(3, "input:b", 5), bigCaps)
	got := put(t, db, ev(4, "input:a", 6), bigCaps)
	if got.Coalesced != 1 || got.Evicted != 0 {
		t.Errorf("appended %+v, want one coalesced", got)
	}
	if s := between(t, db, 0, ^uint64(0), 10); !slices.Equal(s, []uint64{2, 3, 4}) {
		t.Errorf("ring %v, want [2 3 4]", s)
	}
	st, err := db.State(context.Background())
	if err != nil || st.Marks.Coalesced != 1 {
		t.Errorf("coalesced_total %d (%v), want 1", st.Marks.Coalesced, err)
	}
	if got.Size != (ring.Size{Events: 3, Bytes: 16}) {
		t.Errorf("size %+v", got.Size)
	}
}

// defaults.md "Agent event ring cap": the oldest entry goes first whatever
// its class, and each eviction is counted as loss.
func TestTheByteCapEvictsTheOldestAndCountsIt(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	caps := ring.Caps{MaxBytes: 30, MaxAge: bigCaps.MaxAge}
	put(t, db, ev(1, "pane:a", 10), caps)
	put(t, db, ev(2, "", 10), caps)
	put(t, db, ev(3, "", 10), caps)
	got := put(t, db, ev(4, "", 15), caps)
	if got.Evicted != 2 || got.OldestEvicted != 1 {
		t.Errorf("appended %+v, want 2 evicted from seq 1", got)
	}
	if s := between(t, db, 0, ^uint64(0), 10); !slices.Equal(s, []uint64{3, 4}) {
		t.Errorf("ring %v, want [3 4]", s)
	}
	st, err := db.State(context.Background())
	if err != nil || st.Marks.Evicted != 2 {
		t.Errorf("evicted_total %d (%v), want 2", st.Marks.Evicted, err)
	}
}

func TestTheAgeCapEvictsEntriesOlderThanAWeek(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	caps := ring.Caps{MaxBytes: 1 << 20, MaxAge: 7 * 24 * time.Hour}
	old := ev(1, "", 4)
	old.Created = ringT0.Add(-8 * 24 * time.Hour)
	put(t, db, old, caps)
	got := put(t, db, ev(2, "", 4), caps)
	if got.Evicted != 1 || got.OldestEvicted != 1 {
		t.Errorf("appended %+v, want seq 1 evicted for its age", got)
	}
	put(t, db, ev(3, "", 4), caps)
	if s := between(t, db, 0, ^uint64(0), 10); !slices.Equal(s, []uint64{2, 3}) {
		t.Errorf("ring %v, want [2 3]", s)
	}
	st, err := db.State(context.Background())
	if err != nil || st.Marks.Evicted != 1 {
		t.Errorf("evicted_total %d (%v), want 1", st.Marks.Evicted, err)
	}
}

// The hub holds every event at or below a HelloAck floor or an EventAck
// (02i): trimming them is not loss and changes no counter.
func TestTrimDropsEntriesAtOrBelowASeqWithoutCountingLoss(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	for s := uint64(1); s <= 5; s++ {
		put(t, db, ev(s, "", 3), bigCaps)
	}
	n, size, err := db.TrimEvents(context.Background(), 3)
	if err != nil || n != 3 || size != (ring.Size{Events: 2, Bytes: 6}) {
		t.Fatalf("TrimEvents(3) = %d, %+v, %v", n, size, err)
	}
	if n, _, err := db.TrimEvents(context.Background(), 2); err != nil || n != 0 {
		t.Errorf("an older trim removed %d (%v)", n, err)
	}
	st, err := db.State(context.Background())
	if err != nil || st.Marks.Evicted != 0 || st.Marks.Coalesced != 0 {
		t.Errorf("a trim changed the counters: %+v (%v)", st.Marks, err)
	}
}

// Commit order is seq order (the emitter submits under one lock), and the
// file refuses an entry at or below the newest one, so an EventAck trim can
// never take an entry that was not sent yet. An entry never changes.
func TestTheFileRefusesAnEntryBelowTheNewest(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	put(t, db, ev(10, "", 3), bigCaps)
	wait, err := db.SubmitEvent(context.Background(), ev(9, "", 3), bigCaps)
	if err == nil {
		_, err = wait()
	}
	if err == nil {
		t.Fatal("an entry below the newest was saved")
	}
	f := raw(t, dir)
	if _, err := f.ExecContext(context.Background(), `UPDATE event_ring SET event = x'00' WHERE seq = 10`); err == nil {
		t.Error("the file let an entry change")
	}
	if s := between(t, db, 0, ^uint64(0), 10); !slices.Equal(s, []uint64{10}) {
		t.Errorf("ring %v", s)
	}
}

func TestBadRingArgumentsSaveNothing(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	for name, e := range map[string]ring.EventRow{
		"seq 0":    ev(0, "", 3),
		"no bytes": ev(1, "", 0),
		"huge seq": {Seq: 1 << 63, Created: ringT0, Body: []byte("x")},
	} {
		if _, err := db.SubmitEvent(context.Background(), e, bigCaps); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if size, err := db.EventsSize(context.Background()); err != nil || size.Events != 0 {
		t.Errorf("ring %+v (%v), want empty", size, err)
	}
}
