package ingest_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// auditApplier stamps one audit row in the Snapshot job's tx, like a goal
// applier records its writes.
type auditApplier struct{ now time.Time }

func (a auditApplier) Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
	_, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Actor:  statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
		Action: "test.applier", Target: "test/applier", Now: a.now,
	})
	return nil, nil, err
}

// A Snapshot job stamps audits (appliers, rollback) in its own tx: the
// custom hook must publish them through the store's audit publisher in
// writer order, never the tick tail (#403 P1).
func TestSubmitSnapshotHookPublishesAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "6f1a2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("key"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "audit",
		CreatedTime: time.Now(),
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{
		Path: statedb.Path(dir), Name: "state", Migrations: statedb.Migrations(), MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	machineID := "01jauditmachine0000000000"
	now := time.Now().UTC()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id, state,
			hostname_mismatch, created_time) VALUES (?, 'm', 'm', 0, 'install-1', 'ENROLLED', 0, ?)`,
			machineID, now.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Hello(ctx, st, statedb.HelloIn{
		MachineID: machineID, PeerSerial: "c0001", BootID: "boot-1", InstallID: "install-1",
		Peer: "192.0.2.1:5000", Reason: statedb.ConnectStart, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	var got []statedb.AuditStored
	st.SetAuditPublisher(func(rows []statedb.AuditStored) { got = append(got, rows...) })
	p, err := ingest.SubmitSnapshot(ctx, st,
		ingest.Stream{MachineID: machineID, Epoch: 1, InstallID: "install-1"},
		&agentv1.Snapshot{Seq: 11}, []ingest.Applier{auditApplier{now: now}}, now,
		func(statedb.SnapshotOut, []statedb.GoalRow, []statedb.WorkItemRow, []outbox.Closed) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var sawApplier bool
	for _, r := range got {
		if r.Action == "test.applier" {
			sawApplier = true
		}
	}
	if !sawApplier {
		t.Fatalf("hook published %d audits with no test.applier: snapshot audits never published", len(got))
	}
}
