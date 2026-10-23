package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func machineWrite(principal, requestID string, body byte, now time.Time) statedb.MachineWrite {
	return statedb.MachineWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}), Now: now,
	}
}

// enrolledMachineStore is a fresh state.db with one enrolled machine.
func enrolledMachineStore(t *testing.T, id string, at time.Time) (string, *store.Store) {
	t.Helper()
	dir, s := tokenStore(t)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(enrollKey("install-1", 1), id, at), nil); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func TestUpdateMachineOnAMissingMachineIsUnknown(t *testing.T) {
	t.Parallel()
	_, s := tokenStore(t)
	_, err := statedb.UpdateMachine(context.Background(), s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-1", 9, beforeTTL),
		MachineID:    "01jmachine0000000000000001",
		Name:         "renamed",
		HasName:      true,
	}, nil)
	if !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Fatalf("UpdateMachine = %v, want ErrMachineUnknown", err)
	}
}

func TestUpdateMachineRenamesAndLabels(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)

	got, err := statedb.UpdateMachine(ctx, s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-1", 9, at),
		MachineID:    id, Name: "renamed", Labels: []string{"desk"},
		HasName: true, HasLabels: true,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateMachine: %v", err)
	}
	if got.Name != "renamed" || len(got.Labels) != 1 || got.Labels[0] != "desk" {
		t.Errorf("UpdateMachine = %+v, want renamed/[desk]", got)
	}
	if got.Version != 2 || got.Replay {
		t.Errorf("UpdateMachine version=%d replay=%v, want 2 false", got.Version, got.Replay)
	}
	if got.HubSeq == 0 || got.UpdateMs != at.UnixMilli() {
		t.Errorf("UpdateMachine hubSeq=%d updateMs=%d, want stamped", got.HubSeq, got.UpdateMs)
	}

	replay, err := statedb.UpdateMachine(ctx, s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-1", 9, at.Add(time.Minute)),
		MachineID:    id, Name: "renamed", Labels: []string{"desk"},
		HasName: true, HasLabels: true,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateMachine replay: %v", err)
	}
	if !replay.Replay || replay.Version != 2 {
		t.Errorf("replay = version %d replay %v, want 2 true", replay.Version, replay.Replay)
	}

	_, err = statedb.UpdateMachine(ctx, s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-1", 10, at),
		MachineID:    id, Name: "other", HasName: true,
	}, nil)
	if !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("conflict = %v, want ErrRequestConflict", err)
	}

	_, err = statedb.UpdateMachine(ctx, s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-3", 11, at),
		MachineID:    id, ExpectedVersion: 1, Name: "stale", HasName: true,
	}, nil)
	var stale *statedb.VersionStale
	if !errors.As(err, &stale) || stale.Current != 2 {
		t.Errorf("stale = %v, want VersionStale current 2", err)
	}

	var action, target, requestID, detail string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT action, target, request_id, detail FROM audit_log
			WHERE target = ? AND action = ?`, "machines/"+id, "machine.update").
			Scan(&action, &target, &requestID, &detail)
	})
	if err != nil {
		t.Fatalf("machine.update audit row: %v", err)
	}
	if action != "machine.update" || target != "machines/"+id || requestID != "req-1" {
		t.Errorf("audit row %q %q %q", action, target, requestID)
	}
	if detail != `{"display_name":"renamed","version":2}` {
		t.Errorf("audit detail %q", detail)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A slice-3 store (migration 17) gains the machine request log and the
// owner-write stamp at 0; its machines keep their rows, and the next
// owner write stamps update_ms.
func TestOpenMigratesAStoreFromTheOwnerConfigSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:17]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id,
		state, hostname_mismatch, created_time) VALUES ('m1', 'old', 'old', 0, 'i', 'ENROLLED', 0, 1)`); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	var updateMs int64
	if err := s.Read(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(context.Background(), `SELECT update_ms FROM machines
			WHERE machine_id = 'm1'`).Scan(&updateMs); err != nil {
			return err
		}
		var name string
		return tx.QueryRowContext(context.Background(), `SELECT name FROM sqlite_master
			WHERE type = 'table' AND name = 'machine_request_log'`).Scan(&name)
	}); err != nil {
		t.Fatal(err)
	}
	if updateMs != 0 {
		t.Errorf("migrated update_ms = %d, want 0", updateMs)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
