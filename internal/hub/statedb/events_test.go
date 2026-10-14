package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// evAt is the hub time of the events in these tests.
var evAt = snapAt.Add(time.Second)

// snappedStore is enrolledStore with a Hello on epoch 1 and its Snapshot
// at seq 100 applied: events may come.
func snappedStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if _, err := applySnapshot(t, s, snapIn(1, 100, snapAt)); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func evIn(seq, prev uint64) statedb.EventIn {
	return statedb.EventIn{
		MachineID: renewMachineID, Epoch: 1, Seq: seq, PrevSeq: prev, Kind: "pane_exited", Delivery: "never_drop",
		Keep: true, EventTime: evAt.Add(-time.Minute), Body: []byte{0x08, 0x01}, Now: evAt,
	}
}

func applyEvent(t *testing.T, s *store.Store, in statedb.EventIn) (statedb.EventOut, error) {
	t.Helper()
	var out statedb.EventOut
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = statedb.ApplyEvent(context.Background(), tx, in)
		return err
	})
	return out, err
}

type eventRow struct {
	gen, hubSeq, observed      int64
	seq, eventTime             sql.NullInt64
	kind, delivery, detail     string
	replayed, history, hasBody bool
}

func eventRows(t *testing.T, dir string) []eventRow {
	t.Helper()
	rows, err := rawDB(t, dir).QueryContext(t.Context(), `SELECT event_generation, hub_seq, observed_time, seq, event_time,
		kind, delivery, detail, replayed, history_only, body IS NOT NULL FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []eventRow
	for rows.Next() {
		var r eventRow
		if err := rows.Scan(&r.gen, &r.hubSeq, &r.observed, &r.seq, &r.eventTime, &r.kind, &r.delivery, &r.detail,
			&r.replayed, &r.history, &r.hasBody); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The replay rule (02i): an event at or below the Snapshot floor (100) is
// history only, one above it is live; both get a row, move
// hub_last_event_seq and take one hub_seq each, which the event row and the
// machine's status share.
func TestAnEventIsStoredAsHistoryAtOrBelowTheSnapshotFloorAndLiveAbove(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	before := hubSeq(t, dir)
	replay := evIn(100, 0)
	replay.Replayed = true
	out, err := applyEvent(t, s, replay)
	if err != nil {
		t.Fatal(err)
	}
	if out != (statedb.EventOut{Outcome: statedb.EventApplied, History: true, Generation: 1, HubLastEventSeq: 100, Stored: true}) {
		t.Errorf("seq 100: %+v, want applied, history only", out)
	}
	out, err = applyEvent(t, s, evIn(105, 100))
	if err != nil {
		t.Fatal(err)
	}
	if out != (statedb.EventOut{Outcome: statedb.EventApplied, Generation: 1, HubLastEventSeq: 105, Stored: true}) {
		t.Errorf("seq 105: %+v, want applied live", out)
	}
	got := eventRows(t, dir)
	want := []eventRow{
		{
			gen: 1, hubSeq: before + 1, observed: evAt.UnixMilli(), seq: sql.NullInt64{Int64: 100, Valid: true},
			eventTime: sql.NullInt64{Int64: evAt.Add(-time.Minute).UnixMilli(), Valid: true}, kind: "pane_exited",
			delivery: "never_drop", replayed: true, history: true, hasBody: true,
		},
		{
			gen: 1, hubSeq: before + 2, observed: evAt.UnixMilli(), seq: sql.NullInt64{Int64: 105, Valid: true},
			eventTime: sql.NullInt64{Int64: evAt.Add(-time.Minute).UnixMilli(), Valid: true}, kind: "pane_exited",
			delivery: "never_drop", hasBody: true,
		},
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("rows\n got %+v\nwant %+v", got, want)
	}
	m := readSnap(t, dir)
	if m.hubLastSeq != 105 || m.hubSeq != before+2 || m.observed != evAt.UnixMilli() {
		t.Errorf("machine hub_last_event_seq %d hub_seq %d observed %d, want 105, %d, %d", m.hubLastSeq, m.hubSeq,
			m.observed, before+2, evAt.UnixMilli())
	}
}

// An event that only feeds the appliers (SHED, a live COALESCE) gets no
// row but still moves hub_last_event_seq; a re-send at or below it is
// known and writes nothing, not even a hub_seq.
func TestAnEventWithoutARowMovesTheSeqAndAReSendIsKnown(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	in := evIn(120, 0)
	in.Keep, in.Kind, in.Delivery = false, "resource_sample", "shed"
	out, err := applyEvent(t, s, in)
	if err != nil || out.Outcome != statedb.EventApplied || out.Stored || out.HubLastEventSeq != 120 {
		t.Fatalf("shed event: %+v, %v; want applied, no row, seq 120", out, err)
	}
	seq := hubSeq(t, dir)
	for _, again := range []uint64{120, 119, 1} {
		out, err := applyEvent(t, s, evIn(again, 0))
		if err != nil || out.Outcome != statedb.EventKnown || out.HubLastEventSeq != 120 {
			t.Errorf("seq %d: %+v, %v; want known at 120", again, out, err)
		}
	}
	if n := len(eventRows(t, dir)); n != 0 || hubSeq(t, dir) != seq || readSnap(t, dir).hubLastSeq != 120 {
		t.Errorf("%d rows, hub_seq %d (want %d): a known event wrote something", n, hubSeq(t, dir), seq)
	}
}

// The chain: an event whose predecessor on the stream did not commit is
// refused, so a failed job never lets a later one move the seq past it.
func TestAnEventPastAnUncommittedPredecessorIsRefused(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	if _, err := applyEvent(t, s, evIn(101, 0)); err != nil {
		t.Fatal(err)
	}
	// 102 "failed" (never applied); 103 names it as its predecessor.
	if _, err := applyEvent(t, s, evIn(103, 102)); !errors.Is(err, statedb.ErrEventChainBroken) {
		t.Errorf("past a missing predecessor: %v, want ErrEventChainBroken", err)
	}
	if got := readSnap(t, dir).hubLastSeq; got != 101 {
		t.Errorf("hub_last_event_seq %d, want 101", got)
	}
	// A predecessor that was known (at or below the stored seq) is fine.
	if _, err := applyEvent(t, s, evIn(104, 90)); err != nil {
		t.Errorf("after a known predecessor: %v", err)
	}
}

// Events need the live, snapshotted epoch of an existing, unrevoked
// machine; malformed input is refused before the read.
func TestAnEventOffTheCurrentSnapshottedConnectionIsRefused(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if _, err := applyEvent(t, s, evIn(5, 0)); !errors.Is(err, statedb.ErrEventNotCurrent) {
		t.Errorf("before the Snapshot: %v, want ErrEventNotCurrent", err)
	}
	if _, err := applySnapshot(t, s, snapIn(1, 10, snapAt)); err != nil {
		t.Fatal(err)
	}
	mustHello(t, s, hello(1, "boot-b", "192.0.2.8:51000", helloAt.Add(time.Second)))
	if _, err := applyEvent(t, s, evIn(11, 0)); !errors.Is(err, statedb.ErrEventNotCurrent) {
		t.Errorf("displaced epoch 1: %v, want ErrEventNotCurrent", err)
	}
	two := evIn(11, 0)
	two.Epoch = 2
	if _, err := applyEvent(t, s, two); !errors.Is(err, statedb.ErrEventNotCurrent) {
		t.Errorf("epoch 2 before its Snapshot: %v, want ErrEventNotCurrent", err)
	}
	missing := evIn(11, 0)
	missing.MachineID = "01jnosuchmachine0000000000"
	if _, err := applyEvent(t, s, missing); !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Errorf("unknown machine: %v, want ErrMachineUnknown", err)
	}
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET state = 'REVOKED'`); err != nil {
		t.Fatal(err)
	}
	if _, err := applyEvent(t, s, two); !errors.Is(err, statedb.ErrMachineRevoked) {
		t.Errorf("revoked: %v, want ErrMachineRevoked", err)
	}
	for name, mutate := range map[string]func(*statedb.EventIn){
		"seq 0":        func(in *statedb.EventIn) { in.Seq = 0 },
		"seq too big":  func(in *statedb.EventIn) { in.Seq = math.MaxInt64 + 1 },
		"prev too big": func(in *statedb.EventIn) { in.PrevSeq = math.MaxUint64 },
		"no time":      func(in *statedb.EventIn) { in.Now = time.Time{} },
		"no kind":      func(in *statedb.EventIn) { in.Kind = "" },
		"delivery":     func(in *statedb.EventIn) { in.Delivery = "hub" },
	} {
		in := evIn(50, 0)
		mutate(&in)
		if _, err := applyEvent(t, s, in); !errors.Is(err, statedb.ErrEventInvalid) {
			t.Errorf("%s: %v, want ErrEventInvalid", name, err)
		}
	}
	if n := len(eventRows(t, dir)); n != 0 {
		t.Errorf("%d rows after refusals, want 0", n)
	}
}

// A marker row has no seq and no body, carries the machine's generation
// and its own stamp; the table's checks refuse a marker with a seq and an
// event without one, and a second row for one (machine, generation, seq).
func TestMarkersAndTheEventsConstraintsOnTheFile(t *testing.T) {
	t.Parallel()
	dir, s := snappedStore(t)
	before := hubSeq(t, dir)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.AppendMarker(context.Background(), tx, statedb.Marker{
			MachineID: renewMachineID, Kind: statedb.MarkerGap, Detail: `{"from_seq":100}`, Now: evAt,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	got := eventRows(t, dir)
	if len(got) != 1 || got[0] != (eventRow{
		gen: 1, hubSeq: before + 1, observed: evAt.UnixMilli(), kind: "gap",
		delivery: "hub", detail: `{"from_seq":100}`,
	}) {
		t.Errorf("marker rows %+v", got)
	}
	for name, m := range map[string]statedb.Marker{
		"kind":    {MachineID: renewMachineID, Kind: "pane_exited", Now: evAt},
		"machine": {MachineID: "01jnosuchmachine0000000000", Kind: statedb.MarkerGap, Now: evAt},
	} {
		err := s.Write(context.Background(), func(tx *sql.Tx) error { return statedb.AppendMarker(context.Background(), tx, m) })
		if err == nil {
			t.Errorf("%s: marker accepted", name)
		}
	}
	if _, err := applyEvent(t, s, evIn(101, 0)); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for name, q := range map[string]string{
		"duplicate seq": `INSERT INTO events(machine_id, event_generation, seq, kind, delivery, observed_time, hub_seq, body)
			VALUES ('` + renewMachineID + `', 1, 101, 'pane_exited', 'never_drop', 1, 99, x'00')`,
		"marker with seq": `INSERT INTO events(machine_id, event_generation, seq, kind, delivery, observed_time, hub_seq)
			VALUES ('m', 1, 7, 'gap', 'hub', 1, 98)`,
		"event without seq": `INSERT INTO events(machine_id, event_generation, kind, delivery, observed_time, hub_seq, body)
			VALUES ('m', 1, 'pane_exited', 'never_drop', 1, 97, x'00')`,
		"event without body": `INSERT INTO events(machine_id, event_generation, seq, kind, delivery, observed_time, hub_seq)
			VALUES ('m', 1, 7, 'pane_exited', 'never_drop', 1, 96)`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A new generation (an agent rollback) may reuse a seq.
	if _, err := db.ExecContext(t.Context(), `INSERT INTO events(machine_id, event_generation, seq, kind, delivery,
		observed_time, hub_seq, body) VALUES ('`+renewMachineID+`', 2, 101, 'pane_exited', 'never_drop', 1, 95, x'00')`); err != nil {
		t.Errorf("same seq in generation 2: %v", err)
	}
}

// A store from migration 10 (the command expiry indexes) gains the empty
// events table.
func TestOpenMigratesAStoreFromTheCommandExpirySchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := statedb.Path(dir)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:10]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if got := s.SchemaVersion(); got != 13 {
		t.Errorf("schema version %d, want 13", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := rawCount(t, path, `SELECT count(*) FROM events`); n != 0 {
		t.Errorf("events has %d rows", n)
	}
}
