package statedb_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// openWindow sets restore_reconcile_until to until on s (fleetd restore's
// step 4, 02g).
func openWindow(t *testing.T, s *store.Store, until time.Time) {
	t.Helper()
	err := statedb.ApplyRestore(context.Background(), s, statedb.RestoreUpdate{
		SettingsEpoch: sampleIdentity().SettingsEpoch, ReconcileUntil: until,
		Audit: &statedb.AuditEntry{
			Action: "hub.restore", Target: "hub",
			Actor: statedb.Actor{ID: "local:owner", Kind: "LOCAL_USER"},
			Now:   helloAt,
		},
	})
	if err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
}

// A Hello inside the restore's 24 h window is answered with a reconcile
// request exactly once per machine (02g, 03f): the first Hello asks for a
// Snapshot{reconcile}, the next does not.
func TestTheFirstHelloInTheReconcileWindowAsksForASnapshot(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	first := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if !first.Reconcile || first.Adopted {
		t.Errorf("first Hello = %+v, want Reconcile, not Adopted", first)
	}
	second := mustHello(t, s, hello(1, "boot-a", "192.0.2.7:51000", helloAt.Add(time.Minute)))
	if second.Reconcile || second.Adopted {
		t.Errorf("second Hello = %+v, want neither Reconcile nor Adopted", second)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Outside the window no Hello asks for a reconcile snapshot.
func TestHelloOutsideTheReconcileWindowIsNotReconcile(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(-time.Hour))
	out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if out.Reconcile || out.Adopted {
		t.Errorf("Hello after the window = %+v, want neither Reconcile nor Adopted", out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A machine the restored store does not know, with a valid certificate the
// hub never saw, is adopted inside the window (02g): its row is recreated
// ENROLLED under its machine id, its serial is stored, the Hello is
// answered, and the adoption is audited. Outside the window it is still
// unknown.
func TestAnUnknownMachineIsAdoptedInsideTheReconcileWindow(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "cd0002"
	in.PeerNotAfter = helloAt.Add(90 * 24 * time.Hour)
	out := mustHello(t, s, in)
	if out.Epoch != 1 || !out.Reconcile || !out.Adopted {
		t.Fatalf("Hello = %+v, want epoch 1, Reconcile and Adopted", out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	state, epoch, _ := machineMarks(t, db, "01jmachine0000000000000002")
	if state != statedb.MachineEnrolled || epoch != 1 {
		t.Errorf("adopted machine = %s, last_epoch %d; want ENROLLED, 1", state, epoch)
	}
	var name, expected string
	if err := db.QueryRowContext(t.Context(), `SELECT name, expected_hostname FROM machines WHERE machine_id = ?`,
		"01jmachine0000000000000002").Scan(&name, &expected); err != nil {
		t.Fatal(err)
	}
	if name != "01jmachine0000000000000002" || expected != name {
		t.Errorf("adopted name %q, expected hostname %q; want the machine id for both", name, expected)
	}
	var serial, owner string
	var notAfter int64
	if err := db.QueryRowContext(t.Context(), `SELECT serial, machine_id, not_after_time FROM cert_serials WHERE serial = ?`,
		"cd0002").Scan(&serial, &owner, &notAfter); err != nil {
		t.Fatalf("adopted serial: %v", err)
	}
	if owner != "01jmachine0000000000000002" || notAfter != in.PeerNotAfter.UnixMilli() {
		t.Errorf("adopted serial row = %s %s %d", serial, owner, notAfter)
	}
	rows := auditRows(t, dir)
	last := rows[len(rows)-1]
	if last.Action != "machine.adopted" || last.Target != "machines/01jmachine0000000000000002" ||
		last.Machine != "01jmachine0000000000000002" {
		t.Errorf("last audit row = %+v, want machine.adopted for the machine", last)
	}
}

// An adopted machine's next Hello is an ordinary one: no second adoption,
// no second reconcile ask.
func TestAnAdoptedMachinesNextHelloIsOrdinary(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "cd0002"
	in.PeerNotAfter = helloAt.Add(90 * 24 * time.Hour)
	mustHello(t, s, in)
	next := in
	next.LastEpoch, next.Now = 1, helloAt.Add(time.Minute)
	out := mustHello(t, s, next)
	if out.Epoch != 2 || out.Reconcile || out.Adopted {
		t.Errorf("second Hello = %+v, want epoch 2, neither Reconcile nor Adopted", out)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Outside the window an unknown machine is still refused.
func TestAnUnknownMachineOutsideTheWindowIsRefused(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(-time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "cd0002"
	in.PeerNotAfter = helloAt.Add(90 * 24 * time.Hour)
	if _, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, statedb.ErrMachineUnknown) {
		t.Errorf("Hello = %v, want ErrMachineUnknown", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A revoked serial is never adopted, even inside the window.
func TestAdoptionOfARevokedSerialIsRefused(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "cd0002"
	in.PeerNotAfter = helloAt.Add(90 * 24 * time.Hour)
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO cert_serials(serial, machine_id, not_after_time,
			revoked_time, created_time) VALUES ('cd0002', '01jmachine0000000000000002', ?, ?, ?)`,
			in.PeerNotAfter.UnixMilli(), helloAt.UnixMilli(), helloAt.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, statedb.ErrSerialRevoked) {
		t.Errorf("Hello = %v, want ErrSerialRevoked", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// A serial stored for another machine is never adopted as this one's.
func TestAdoptionOfAnotherMachinesSerialIsRefused(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "ab0001"
	in.PeerNotAfter = helloAt.Add(90 * 24 * time.Hour)
	if _, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, statedb.ErrPeerSerialUnknown) {
		t.Errorf("Hello = %v, want ErrPeerSerialUnknown", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// Adoption stores the presented serial, so it needs the certificate's
// expiry from the handshake.
func TestAdoptionWithoutTheSerialExpiryIsRefused(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	openWindow(t, s, helloAt.Add(time.Hour))
	in := hello(0, "boot-b", "192.0.2.8:51000", helloAt)
	in.MachineID, in.PeerSerial = "01jmachine0000000000000002", "cd0002"
	if _, err := statedb.Hello(context.Background(), s, in); !errors.Is(err, statedb.ErrHelloInvalid) {
		t.Errorf("Hello = %v, want ErrHelloInvalid", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
