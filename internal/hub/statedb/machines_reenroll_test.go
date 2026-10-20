package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func previousToken(principal, requestID string, body byte, previous string) statedb.EnrollToken {
	tok := sampleToken(principal, requestID, body)
	tok.PreviousMachine = previous
	return tok
}

func TestInsertEnrollTokenPreviousMachineChecksEligibility(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)

	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "rev-1", 9, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}
	if err := statedb.InsertEnrollToken(ctx, s, previousToken("local:alice", "tok-1", 21, id)); err != nil {
		t.Errorf("previous_machine on a REVOKED machine: %v", err)
	}
	if err := statedb.InsertEnrollToken(ctx, s, previousToken("local:alice", "tok-2", 22,
		"01jmissing0000000000000001")); !errors.Is(err, statedb.ErrNotReenrollable) {
		t.Errorf("previous_machine on a missing machine = %v, want ErrNotReenrollable", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertEnrollTokenPreviousMachineRefusesALiveMachine(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	if err := statedb.InsertEnrollToken(ctx, s, previousToken("local:alice", "tok-1", 21, id)); !errors.Is(err,
		statedb.ErrNotReenrollable) {
		t.Errorf("previous_machine on a live machine = %v, want ErrNotReenrollable", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckEnrollPreviousMachineReturnsTheToken(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)
	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "rev-1", 9, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}
	tok := previousToken("local:alice", "tok-1", 21, id)
	if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
		t.Fatal(err)
	}
	check, err := statedb.CheckEnroll(ctx, s, statedb.EnrollKey{
		SecretHash: tok.SecretHash, InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte{2}),
	}, at)
	if err != nil {
		t.Fatalf("CheckEnroll: %v", err)
	}
	if check.Replay != nil || check.Token.PreviousMachine != id {
		t.Errorf("CheckEnroll = %+v, want the token naming %q", check, id)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func reenrollMachine(key statedb.EnrollKey, previous, serial string, at time.Time) statedb.ReenrollMachine {
	return statedb.ReenrollMachine{
		Key: key, PreviousMachine: previous, CertDER: []byte("cert-new-" + previous), Serial: serial,
		CertNotAfter: at.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan",
		Facts: `{"hostname":"laptop.lan"}`, Now: at,
	}
}

func TestEnrollPreviousMachineKeepsTheID(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)
	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "rev-1", 9, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}
	tok := previousToken("local:alice", "tok-1", 21, id)
	if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte{2})}
	got, err := statedb.EnrollPrevious(ctx, s, reenrollMachine(key, id, "cd0001", at.Add(time.Minute)))
	if err != nil {
		t.Fatalf("EnrollPrevious: %v", err)
	}
	if got.MachineID != id || got.Replayed {
		t.Errorf("EnrollPrevious = %+v, want the same id fresh", got)
	}
	var state string
	var version int64
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT state, version FROM machines WHERE machine_id = ?`,
			id).Scan(&state, &version); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM cert_serials WHERE machine_id = ?`, id).
			Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("cert_serials rows = %d, want 2 (revoked serial stays)", n)
		}
		return tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE action = 'machine.enroll'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "ENROLLED" || version != 3 {
		t.Errorf("row = %q v%d, want ENROLLED v3", state, version)
	}

	replay, err := statedb.CheckEnroll(ctx, s, key, at.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("CheckEnroll replay: %v", err)
	}
	if replay.Replay == nil || replay.Replay.MachineID != id {
		t.Errorf("replay = %+v, want the stored response", replay)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollPreviousMismatchBurnsNothing(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	other := "01jmachine0000000000000002"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)
	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "rev-1", 9, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}
	tok := previousToken("local:alice", "tok-1", 21, id)
	if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte{2})}
	in := reenrollMachine(key, id, "cd0001", at.Add(time.Minute))
	in.RequestPrevious = other
	if _, err := statedb.EnrollPrevious(ctx, s, in); !errors.Is(err, statedb.ErrPreviousMismatch) {
		t.Fatalf("mismatch = %v, want ErrPreviousMismatch", err)
	}
	check, err := statedb.CheckEnroll(ctx, s, key, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("token after mismatch: %v", err)
	}
	if check.Token.PreviousMachine != id {
		t.Errorf("token after mismatch = %+v, want it unburned", check)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollPreviousGoneFallsBackToFresh(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx := context.Background()
	at := beforeTTL.Add(time.Minute)
	if _, err := statedb.RevokeMachine(ctx, s, statedb.MachineRevoke{
		MachineWrite: machineWrite("local:alice", "rev-1", 9, at),
		MachineID:    id, CloseOpen: closeForRevoke,
	}); err != nil {
		t.Fatal(err)
	}
	tok := previousToken("local:alice", "tok-1", 21, id)
	if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
		t.Fatal(err)
	}
	if _, _, err := statedb.DeleteMachine(ctx, s, statedb.MachineDelete{
		MachineWrite: machineWrite("local:alice", "del-1", 12, at),
		MachineID:    id,
	}); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte{2})}
	if _, err := statedb.EnrollPrevious(ctx, s, reenrollMachine(key, id, "cd0001", at.Add(time.Minute))); !errors.Is(
		err, statedb.ErrPreviousGone) {
		t.Fatalf("gone = %v, want ErrPreviousGone", err)
	}
	check, err := statedb.CheckEnroll(ctx, s, key, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("CheckEnroll after gone: %v", err)
	}
	if check.Token.PreviousMachine != "" {
		t.Errorf("token after gone = %+v, want a fresh token", check)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
