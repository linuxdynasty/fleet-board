package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// closeForRevoke is the test stand-in for the revoke's outbox closer:
// the real outbox.CloseForRevoke mapped onto statedb.RevokedCommand.
func closeForRevoke(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) ([]statedb.RevokedCommand, error) {
	closed, err := outbox.CloseForRevoke(ctx, tx, machineID, now)
	if err != nil {
		return nil, err
	}
	out := make([]statedb.RevokedCommand, 0, len(closed))
	for _, c := range closed {
		out = append(out, statedb.RevokedCommand{
			CommandID: c.CommandID, MachineID: c.MachineID, Kind: c.Kind, From: c.From, Created: c.Created,
		})
	}
	return out, nil
}

func insertOpenCommand(t *testing.T, s *store.Store, cmdID, machineID string, now time.Time) {
	t.Helper()
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO commands(command_id, machine_id, kind, command,
			create_time, not_after_time, update_time, hub_seq) VALUES (?, ?, 'session.keys', ?, ?, ?, ?, 1)`,
			cmdID, machineID, []byte("{}"), now.UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRevokeMachineRevokesSerialsAndClosesCommands(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)
	insertOpenCommand(t, s, "cmd-1", id, at)

	got, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "req-1", 9, at),
		MachineID:    id, Reason: "stolen laptop",
		CloseOpen: closeForRevoke,
	})
	if err != nil {
		t.Fatalf("RevokeMachine: %v", err)
	}
	if len(got.Closed) != 1 || got.Closed[0].CommandID != "cmd-1" {
		t.Errorf("closed = %+v, want [cmd-1]", got.Closed)
	}
	if got.Machine.State != "REVOKED" || got.Machine.Version != 2 {
		t.Errorf("revoke = state %q version %d, want REVOKED 2", got.Machine.State, got.Machine.Version)
	}
	if !got.Machine.Revoked || got.Machine.RevokeReason != "stolen laptop" {
		t.Errorf("revocation = %+v, want reason kept", got.Machine)
	}
	if len(got.Serials) != 1 || got.Serials[0].Serial != "ab0001" {
		t.Errorf("serials = %+v, want [ab0001]", got.Serials)
	}
	var state, status, reason string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT revoked_time FROM cert_serials WHERE serial = 'ab0001'`).
			Scan(new(int64)); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT state, status, reason FROM commands WHERE command_id = 'cmd-1'`).
			Scan(&state, &status, &reason)
	})
	if err != nil {
		t.Fatalf("revoke effects: %v", err)
	}
	if state != "done" || status != "EXPIRED" || reason != "MACHINE_REVOKED" {
		t.Errorf("command = %q %q %q, want done EXPIRED MACHINE_REVOKED", state, status, reason)
	}

	again, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "req-2", 10, at.Add(time.Minute)),
		MachineID:    id, Reason: "stolen laptop",
		CloseOpen: closeForRevoke,
	})
	if err != nil {
		t.Fatalf("second RevokeMachine: %v", err)
	}
	if again.Machine.Version != 2 || again.Machine.State != "REVOKED" {
		t.Errorf("second revoke = %+v, want the revoked machine itself", again.Machine)
	}
	var revokes int
	err = s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'machine.revoke'`).Scan(&revokes)
	})
	if err != nil || revokes != 1 {
		t.Errorf("machine.revoke rows = %d, %v; want 1 (idempotent)", revokes, err)
	}

	_, err = statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "req-3", 11, at),
		MachineID:    "01jmissing0000000000000001",
		CloseOpen:    closeForRevoke,
	})
	if !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Errorf("missing = %v, want ErrMachineUnknown", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteMachineNeedsARevokedMachine(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)

	_, err := statedb.DeleteMachine(ctx, s, statedb.MachineDelete{
		MachineWrite: machineWrite("local:alice", "req-1", 9, at),
		MachineID:    id,
	})
	if !errors.Is(err, statedb.ErrMachineNotRevoked) {
		t.Fatalf("live delete = %v, want ErrMachineNotRevoked", err)
	}
	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "req-2", 10, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}

	gone, err := statedb.DeleteMachine(ctx, s, statedb.MachineDelete{
		MachineWrite: machineWrite("local:alice", "req-3", 11, at.Add(time.Minute)),
		MachineID:    id,
	})
	if err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	if !gone.Deleted || gone.Replay {
		t.Errorf("delete = %+v, want deleted without replay", gone)
	}

	err = s.Read(ctx, func(tx *sql.Tx) error {
		count := func(table string) int {
			t.Helper()
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE machine_id = ?`,
				id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}
		for _, table := range []string{"machines", "machine_connections", "commands", "enroll_responses"} {
			if n := count(table); n != 0 {
				t.Errorf("%s rows = %d, want 0", table, n)
			}
		}
		if n := count("cert_serials"); n != 1 {
			t.Errorf("cert_serials rows = %d, want 1 (revocation outlives delete)", n)
		}
		var audit int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE target = ?`,
			"machines/"+id).Scan(&audit); err != nil {
			return err
		}
		if audit != 3 {
			t.Errorf("audit rows = %d, want 3 (enroll, revoke, delete)", audit)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("delete effects: %v", err)
	}

	again, err := statedb.DeleteMachine(ctx, s, statedb.MachineDelete{
		MachineWrite: machineWrite("local:alice", "req-3", 11, at.Add(2*time.Minute)),
		MachineID:    id,
	})
	if err != nil {
		t.Fatalf("replayed DeleteMachine: %v", err)
	}
	if !again.Deleted || !again.Replay {
		t.Errorf("replay = %+v, want deleted replay", again)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
