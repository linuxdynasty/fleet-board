package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// snapAt is the hub time of the first Snapshot in these tests.
var snapAt = helloAt.Add(time.Second)

// snapIn is a Snapshot for the machine enrolledStore made, on epoch, with
// the Hello values of an agent that never lost state.
func snapIn(epoch, seq uint64, at time.Time) statedb.SnapshotIn {
	return statedb.SnapshotIn{
		MachineID: renewMachineID, Epoch: epoch, Seq: seq, InstallID: "install-1",
		Facts:     `{"hostname":"laptop","os":"darwin"}`,
		Resources: `{"load1":0.5}`,
		Policy: statedb.Policy{
			Hash: "h1", SchemaVersion: "1", Loaded: at.Add(-time.Hour), ChangedSinceLoad: true, SafeMode: true,
			Error: "line 3: bad key",
		},
		Drain: statedb.Drain{Draining: true, Local: true},
		Now:   at,
	}
}

// applySnapshot runs ApplySnapshot and MarkOnline as one writer job, as
// the gateway's Snapshot job does.
func applySnapshot(t *testing.T, s *store.Store, in statedb.SnapshotIn) (statedb.SnapshotOut, error) {
	t.Helper()
	var out statedb.SnapshotOut
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		if out, err = statedb.ApplySnapshot(context.Background(), tx, in); err != nil {
			return err
		}
		_, _, err = statedb.MarkOnline(context.Background(), tx, in.MachineID, in.Now)
		return err
	})
	return out, err
}

// snapRow is what the Snapshot transaction writes to the machine's row.
type snapRow struct {
	state, facts, install, policyHash, policySchema, policyError string
	resources                                                    sql.NullString
	policyLoaded, lastSeen                                       sql.NullInt64
	snapSeq, snapEpoch, generation, hubLastSeq                   int64
	shed, coalesced, evicted, hubSeq, observed                   int64
	changed, safe, draining, local                               bool
}

func readSnap(t *testing.T, dir string) snapRow {
	t.Helper()
	var r snapRow
	err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT state, facts, install_id, policy_hash, policy_schema_version,
		policy_error, resources, policy_loaded_time, last_seen_time, snapshot_seq, snapshot_epoch, event_generation,
		hub_last_event_seq, events_shed_total, events_coalesced_total, events_evicted_total, hub_seq, observed_time,
		policy_changed_since_load, policy_safe_mode, agent_draining, agent_drain_local FROM machines WHERE machine_id = ?`,
		renewMachineID).Scan(&r.state, &r.facts, &r.install, &r.policyHash, &r.policySchema, &r.policyError, &r.resources,
		&r.policyLoaded, &r.lastSeen, &r.snapSeq, &r.snapEpoch, &r.generation, &r.hubLastSeq, &r.shed, &r.coalesced,
		&r.evicted, &r.hubSeq, &r.observed, &r.changed, &r.safe, &r.draining, &r.local)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The Snapshot of the current epoch stores the facts, resources, policy,
// drain, seq floor and the Hello's counters, marks the machine ONLINE and
// stamps it; Hello alone left it ENROLLED. No rollback, no audit row.
func TestTheSnapshotOfTheCurrentEpochIsStoredAndMarksTheMachineOnline(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if got := readSnap(t, dir); got.state != statedb.MachineEnrolled || got.snapEpoch != 0 {
		t.Fatalf("after Hello: state %s, snapshot_epoch %d; want ENROLLED, 0", got.state, got.snapEpoch)
	}
	before, rows := hubSeq(t, dir), len(auditRows(t, dir))
	in := snapIn(1, 42, snapAt)
	in.Counters = statedb.EventCounters{Shed: 3, Coalesced: 2, Evicted: 1}
	out, err := applySnapshot(t, s, in)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if out.Generation != 1 || len(out.Rollback) != 0 {
		t.Errorf("out = %+v, want generation 1 and no rollback", out)
	}
	got := readSnap(t, dir)
	want := snapRow{
		state: "ONLINE", facts: in.Facts, install: "install-1", policyHash: "h1", policySchema: "1", policyError: "line 3: bad key",
		resources:    sql.NullString{String: `{"load1":0.5}`, Valid: true},
		policyLoaded: sql.NullInt64{Int64: snapAt.Add(-time.Hour).UnixMilli(), Valid: true},
		lastSeen:     sql.NullInt64{Int64: snapAt.UnixMilli(), Valid: true},
		snapSeq:      42, snapEpoch: 1, generation: 1, shed: 3, coalesced: 2, evicted: 1, hubSeq: before + 1,
		observed: snapAt.UnixMilli(), changed: true, safe: true, draining: true, local: true,
	}
	if got != want {
		t.Errorf("row\n got %+v\nwant %+v", got, want)
	}
	if n := len(auditRows(t, dir)); n != rows {
		t.Errorf("%d audit rows, want %d (no rollback)", n, rows)
	}
}

// A second Snapshot on the same stream (an answer to SnapshotRequest)
// replaces the facts; a snapshot without resources stores them as unknown
// (NULL), never as zeros.
func TestASecondSnapshotReplacesTheFacts(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if _, err := applySnapshot(t, s, snapIn(1, 42, snapAt)); err != nil {
		t.Fatal(err)
	}
	in := snapIn(1, 43, snapAt.Add(time.Second))
	in.Facts, in.Resources, in.Policy, in.Drain = `{"hostname":"laptop2"}`, "", statedb.Policy{}, statedb.Drain{}
	if _, err := applySnapshot(t, s, in); err != nil {
		t.Fatal(err)
	}
	got := readSnap(t, dir)
	if got.facts != `{"hostname":"laptop2"}` || got.resources.Valid || got.snapSeq != 43 || got.policyHash != "" ||
		got.policyLoaded.Valid || got.safe || got.draining {
		t.Errorf("row after the second snapshot %+v, want the new facts, NULL resources, seq 43, empty policy", got)
	}
}

// A Snapshot that is not the machine's current epoch changes nothing: an
// older stream's after a newer Hello, and the live epoch's after its row
// closed. A REVOKED or missing machine is refused too.
func TestASnapshotOfAnotherEpochOrMachineIsRefused(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	mustHello(t, s, hello(1, "boot-b", "192.0.2.8:51000", helloAt.Add(time.Second)))
	before := readSnap(t, dir)
	seq := hubSeq(t, dir)
	if _, err := applySnapshot(t, s, snapIn(1, 42, snapAt)); !errors.Is(err, statedb.ErrSnapshotNotCurrent) {
		t.Errorf("snapshot of displaced epoch 1: %v, want ErrSnapshotNotCurrent", err)
	}
	if _, err := statedb.EndConnection(context.Background(), s, renewMachineID, 2, statedb.EndStreamClosed, snapAt); err != nil {
		t.Fatal(err)
	}
	before.lastSeen = readSnap(t, dir).lastSeen // EndConnection moves it
	if _, err := applySnapshot(t, s, snapIn(2, 42, snapAt)); !errors.Is(err, statedb.ErrSnapshotNotCurrent) {
		t.Errorf("snapshot of closed epoch 2: %v, want ErrSnapshotNotCurrent", err)
	}
	if got := readSnap(t, dir); got != before || hubSeq(t, dir) != seq {
		t.Errorf("a refused snapshot changed the row\n got %+v\nwant %+v", got, before)
	}
	missing := snapIn(1, 1, snapAt)
	missing.MachineID = "01jnosuchmachine0000000000"
	if _, err := applySnapshot(t, s, missing); !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Errorf("unknown machine: %v, want ErrMachineUnknown", err)
	}
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET state = 'REVOKED'`); err != nil {
		t.Fatal(err)
	}
	mustRefuse := snapIn(3, 50, snapAt)
	if _, err := applySnapshot(t, s, mustRefuse); !errors.Is(err, statedb.ErrMachineRevoked) {
		t.Errorf("revoked machine: %v, want ErrMachineRevoked", err)
	}
}

// Values the store cannot hold (above 2^63 - 1) are refused before
// anything is written.
func TestASnapshotWithAnOutOfRangeNumberIsRefused(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	for name, mutate := range map[string]func(*statedb.SnapshotIn){
		"seq":            func(in *statedb.SnapshotIn) { in.Seq = math.MaxInt64 + 1 },
		"last_event_seq": func(in *statedb.SnapshotIn) { in.LastEventSeq = math.MaxUint64 },
		"evicted":        func(in *statedb.SnapshotIn) { in.Counters.Evicted = math.MaxInt64 + 1 },
		"zero time":      func(in *statedb.SnapshotIn) { in.Now = time.Time{} },
	} {
		in := snapIn(1, 42, snapAt)
		mutate(&in)
		if _, err := applySnapshot(t, s, in); !errors.Is(err, statedb.ErrSnapshotInvalid) {
			t.Errorf("%s: %v, want ErrSnapshotInvalid", name, err)
		}
	}
}

// A state.db from 5j (migrations 1 to 7) gains the Snapshot columns with
// their defaults: generation 1, every seq, counter and flag 0, no
// resources.
func TestOpenAddsTheSnapshotColumnsToARestoreSchemaStore(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := statedb.Path(dir)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:7]})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO machines(machine_id, name, expected_hostname, drained,
			install_id, state, hostname_mismatch, created_time) VALUES ('m1', 'laptop', 'laptop', 0, 'i1', 'OFFLINE', 0, 0)`)
		return err
	})
	if err := errors.Join(err, s.Close()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := open(t, dir).Close(); err != nil {
		t.Fatal(err)
	}
	n := rawCount(t, path, `SELECT count(*) FROM machines WHERE hub_last_event_seq = 0 AND event_generation = 1
		AND snapshot_seq = 0 AND snapshot_epoch = 0 AND events_shed_total = 0 AND events_coalesced_total = 0
		AND events_evicted_total = 0 AND resources IS NULL AND policy_hash = '' AND policy_schema_version = ''
		AND policy_loaded_time IS NULL AND policy_changed_since_load = 0 AND policy_safe_mode = 0 AND policy_error = ''
		AND agent_draining = 0 AND agent_drain_local = 0 AND state = 'OFFLINE'`)
	if n != 1 {
		t.Errorf("%d migrated machine rows with the snapshot defaults, want 1", n)
	}
	if v := statedb.Migrations()[len(statedb.Migrations())-1]; v.Version != 8 || v.Name != "machine_snapshot" {
		t.Errorf("last migration %d %q, want 8 machine_snapshot", v.Version, v.Name)
	}
}

// HelloAck.hub_last_event_seq is the stored hub_last_event_seq, never the
// Snapshot's seq floor.
func TestHelloReturnsTheStoredEventSeq(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET hub_last_event_seq = 77, snapshot_seq = 90`); err != nil {
		t.Fatal(err)
	}
	if out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt)); out.HubLastEventSeq != 77 {
		t.Errorf("HubLastEventSeq = %d, want 77", out.HubLastEventSeq)
	}
}

// ReadSnapshot returns what the Snapshot transaction stored, on the read
// pool; an unknown machine is ErrMachineUnknown.
func TestReadSnapshotReturnsTheStoredSnapshot(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	in := snapIn(1, 42, snapAt)
	in.Counters = statedb.EventCounters{Shed: 3, Coalesced: 2, Evicted: 1}
	in.Drain.Local, in.Policy.SafeMode = false, false // every bool differs from its neighbour
	if _, err := applySnapshot(t, s, in); err != nil {
		t.Fatal(err)
	}
	got, err := statedb.ReadSnapshot(context.Background(), s, renewMachineID)
	if err != nil {
		t.Fatal(err)
	}
	want := statedb.MachineSnapshot{
		State: "ONLINE", Facts: in.Facts, Resources: `{"load1":0.5}`, Policy: in.Policy, Drain: in.Drain,
		Counters: in.Counters, InstallID: "install-1", EventGeneration: 1, SnapshotSeq: 42, SnapshotEpoch: 1,
	}
	want.Policy.Loaded = time.UnixMilli(snapAt.Add(-time.Hour).UnixMilli())
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadSnapshot\n got %+v\nwant %+v", got, want)
	}
	if _, err := statedb.ReadSnapshot(context.Background(), s, "01jnosuchmachine0000000000"); !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Errorf("unknown machine: %v, want ErrMachineUnknown", err)
	}
}
