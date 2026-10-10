package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Many Hellos for one machine at once: the single writer runs them one
// after another, so the epochs are 1..n, each displaces the one before it,
// and exactly one row is live.
func TestConcurrentHellosForOneMachineLeaveOneLiveConnection(t *testing.T) {
	t.Parallel()
	const n = 48
	dir, s := enrolledStore(t)
	outs := make([]statedb.HelloOut, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			outs[i], errs[i] = statedb.Hello(context.Background(), s,
				hello(0, fmt.Sprintf("boot-%02d", i), "192.0.2.7:51000", helloAt))
		})
	}
	wg.Wait()
	seen := map[uint64]bool{}
	displaced := map[uint64]uint64{} // displaced epoch -> the epoch that displaced it
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("Hello %d: %v", i, errs[i])
		}
		e := outs[i].Epoch
		if e < 1 || e > n || seen[e] {
			t.Fatalf("epoch %d is out of 1..%d or given twice", e, n)
		}
		seen[e] = true
		if d := outs[i].Displaced; d != nil {
			displaced[d.Epoch] = e
		}
	}
	// Serialised, each Hello displaced the one just before it; the first
	// displaced nothing.
	if len(displaced) != n-1 {
		t.Fatalf("%d Hellos displaced a connection, want %d", len(displaced), n-1)
	}
	for old, by := range displaced {
		if by != old+1 {
			t.Errorf("epoch %d was displaced by %d, want %d", old, by, old+1)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := statedb.Path(dir)
	if live := rawCount(t, path, `SELECT count(*) FROM machine_connections WHERE ended_time IS NULL`); live != 1 {
		t.Errorf("%d live rows, want 1", live)
	}
	if e := rawCount(t, path, `SELECT epoch FROM machine_connections WHERE ended_time IS NULL`); e != n {
		t.Errorf("the live row is epoch %d, want %d", e, n)
	}
	if bad := rawCount(t, path, `SELECT count(*) FROM machine_connections
		WHERE ended_time IS NOT NULL AND (end_reason <> 'displaced' OR displaced_by <> epoch + 1)`); bad != 0 {
		t.Errorf("%d closed rows not displaced by the next epoch", bad)
	}
}

const otherMachineID = "01jmachine0000000000000002"

// enrollOther enrolls a second machine with its own token (serial ab0002).
func enrollOther(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := statedb.InsertEnrollToken(ctx, s, sampleToken("local:alice", "req-2", 2)); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: sha256.Sum256([]byte{2, 1, 2, 3}), InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte{9})}
	if _, err := statedb.Enroll(ctx, s, newMachine(key, otherMachineID, beforeTTL)); err != nil {
		t.Fatal(err)
	}
}

// A Hello or a close for machine A never reads or writes machine B's rows,
// even when A's epochs equal B's.
func TestHelloForOneMachineNeverTouchesAnother(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	enrollOther(t, s)
	ctx := context.Background()
	other := hello(0, "boot-z", "203.0.113.9:4000", helloAt)
	other.MachineID, other.PeerSerial, other.InstallID = otherMachineID, "ab0002", "install-2"
	if out := mustHello(t, s, other); out.Epoch != 1 {
		t.Fatalf("B's first epoch %d, want 1", out.Epoch)
	}
	// A's epoch 1, then epoch 2 displacing A's epoch 1, while B's epoch 1 is live.
	for i := range 2 {
		out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt.Add(time.Duration(i+1)*time.Second)))
		if out.Epoch != uint64(i+1) || (out.Displaced != nil && out.Displaced.Peer == "203.0.113.9:4000") {
			t.Fatalf("A's Hello %d = %+v", i, out)
		}
	}
	// A's epoch 1 is closed already; B's epoch 1 is live and must stay so.
	if live, err := statedb.EndConnection(ctx, s, renewMachineID, 1, statedb.EndStreamClosed, helloAt.Add(time.Minute)); err != nil || live {
		t.Errorf("EndConnection(A, epoch 1) = %v, %v; want a no-op", live, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if r := readConn(t, db, otherMachineID, 1); r.ended.Valid || r.started != helloAt.UnixMilli() {
		t.Errorf("B's row changed: %+v", r)
	}
	if _, epoch, seen := machineMarks(t, db, otherMachineID); epoch != 1 || seen.Int64 != helloAt.UnixMilli() {
		t.Errorf("B's marks changed: last_epoch %d, last_seen %v", epoch, seen)
	}
	if _, epoch, _ := machineMarks(t, db, renewMachineID); epoch != 2 {
		t.Errorf("A's last_epoch %d, want 2", epoch)
	}
}

func TestMachineConnectionsConstraintsOnTheFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db := rawDB(t, dir)
	ctx := t.Context()
	insert := func(epoch int64, ended any, end, displacedBy, age any) error {
		_, err := db.ExecContext(ctx, `INSERT INTO machine_connections(machine_id, epoch, boot_id, install_id, peer,
			connect_reason, started_time, ended_time, end_reason, displaced_by, last_heartbeat_age_ms)
			VALUES ('m1', ?, 'b', 'i', 'p', 'start', 1, ?, ?, ?, ?)`, epoch, ended, end, displacedBy, age)
		return err
	}
	if err := insert(1, 5, "displaced", 2, 100); err != nil {
		t.Fatalf("a displaced row: %v", err)
	}
	if err := insert(2, 6, "stream_closed", nil, nil); err != nil {
		t.Fatalf("a closed row: %v", err)
	}
	if err := insert(3, nil, nil, nil, nil); err != nil {
		t.Fatalf("a live row: %v", err)
	}
	refused := map[string]func() error{
		"second live row":          func() error { return insert(4, nil, nil, nil, nil) },
		"same machine and epoch":   func() error { return insert(1, 7, "offline", nil, nil) },
		"epoch 0":                  func() error { return insert(0, 7, "offline", nil, nil) },
		"ended without a reason":   func() error { return insert(5, 7, nil, nil, nil) },
		"reason while live":        func() error { return insert(5, nil, "offline", nil, nil) },
		"unknown end reason":       func() error { return insert(5, 7, "gone", nil, nil) },
		"displaced without by":     func() error { return insert(5, 7, "displaced", nil, nil) },
		"by without displaced":     func() error { return insert(5, 7, "offline", 6, nil) },
		"displaced by a lower one": func() error { return insert(5, 7, "displaced", 4, nil) },
		"age without displacement": func() error { return insert(5, 7, "offline", nil, 10) },
		"negative heartbeat age":   func() error { return insert(5, 7, "displaced", 6, -1) },
		"epoch past the agent's":   func() error { return insert(9223372036854775807, 7, "offline", nil, nil) },
		"ended before it started":  func() error { return insert(5, 0, "offline", nil, nil) },
		"unknown connect reason": func() error {
			_, err := db.ExecContext(ctx, `INSERT INTO machine_connections(machine_id, epoch, boot_id, install_id, peer,
				connect_reason, started_time) VALUES ('m2', 1, 'b', 'i', 'p', 'boot', 1)`)
			return err
		},
	}
	for name, run := range refused {
		if err := run(); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: %v, want a constraint error", name, err)
		}
	}
}

// A state.db from plan row 4 (migrations 1 to 4) with an enrolled machine
// gains the connection table; the machine starts at last_epoch 0.
func TestOpenMigratesAStoreFromTheRenewalsSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	ctx := context.Background()
	s, err := store.Open(ctx, store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:4]})
	if err != nil {
		t.Fatal(err)
	}
	id := sampleIdentity()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO hub_identity(id, fleet_id, ca_cert, ca_key, server_cert, server_key,
			server_names, account_salt, salt_version, settings_epoch, created_time) VALUES (1, ?, x'01', x'02', x'03', x'04', '[]', ?, 1, ?, 1)`,
			id.FleetID, id.AccountSalt, id.SettingsEpoch)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Raw SQL, not Enroll: Enroll's audit row needs the 5d tables, which a
	// migration 4 store does not have.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id,
			state, hostname_mismatch, created_time) VALUES (?, 'old', 'old', 0, 'install-1', 'ENROLLED', 0, 1)`, renewMachineID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if got := s.SchemaVersion(); got != len(statedb.Migrations()) {
		t.Errorf("schema version %d, want %d", got, len(statedb.Migrations()))
	}
	if state, epoch, seen := machineMarks(t, rawDB(t, dir), renewMachineID); state != statedb.MachineEnrolled || epoch != 0 || seen.Valid {
		t.Errorf("the migrated machine = %s, last_epoch %d, last_seen %v; want ENROLLED, 0, NULL", state, epoch, seen)
	}
	out := mustHello(t, s, hello(0, "boot-a", "192.0.2.7:51000", helloAt))
	if out.Epoch != 1 || out.SettingsEpoch != id.SettingsEpoch {
		t.Errorf("Hello after migration = %+v", out)
	}
}

// The hub's epoch ceiling is the agent's: the next epoch the hub can give
// is one agent.db accepts.
func TestMaxEpochIsTheAgentsCeiling(t *testing.T) {
	t.Parallel()
	if statedb.MaxEpoch != ring.MaxEpoch {
		t.Errorf("statedb.MaxEpoch %d, ring.MaxEpoch %d", statedb.MaxEpoch, ring.MaxEpoch)
	}
	if err := ring.CheckHubMarks(statedb.MaxEpoch, 0); err != nil {
		t.Errorf("the agent refuses the hub's largest epoch: %v", err)
	}
}
