package statedb_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

const renewMachineID = "01jmachine0000000000000001"

// enrolledStore is a state.db with one enrolled machine whose certificate
// serial is ab0001 (newMachine).
func enrolledStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir, s := tokenStore(t)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(enrollKey("install-1", 1), renewMachineID, beforeTTL)); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

// renewal asks for a certificate for CSR key k, presenting serial ab0001;
// signs counts the calls to Sign, which answers serial and its not_after.
func renewal(k byte, serial string, signs *atomic.Int32) statedb.Renewal {
	return statedb.Renewal{
		MachineID: renewMachineID, PeerSerial: "ab0001", CSRKeyHash: sha256.Sum256([]byte{k}), Now: beforeTTL,
		Sign: func() (statedb.Signed, error) {
			signs.Add(1)
			return statedb.Signed{CertDER: []byte("cert-" + serial), Serial: serial, NotAfter: beforeTTL.Add(90 * 24 * time.Hour)}, nil
		},
	}
}

func TestRenewStoresTheCertificateThenReplaysIt(t *testing.T) {
	t.Parallel()
	dir, s := enrolledStore(t)
	var signs atomic.Int32
	got, err := statedb.Renew(context.Background(), s, renewal(2, "cd01", &signs))
	if err != nil || got.Replayed || got.Serial != "cd01" || string(got.CertDER) != "cert-cd01" {
		t.Fatalf("Renew = %+v, %v; want a fresh cd01", got, err)
	}
	again, err := statedb.Renew(context.Background(), s, renewal(2, "ef02", &signs))
	if err != nil || !again.Replayed || again.Serial != "cd01" || !bytes.Equal(again.CertDER, got.CertDER) {
		t.Fatalf("repeat Renew = %+v, %v; want the stored cd01 replayed", again, err)
	}
	if n := signs.Load(); n != 1 {
		t.Errorf("Sign ran %d times, want 1", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	var machine string
	var end int64
	if err := db.QueryRowContext(t.Context(), `SELECT machine_id, not_after_time FROM cert_serials WHERE serial = 'cd01'`).
		Scan(&machine, &end); err != nil {
		t.Fatal(err)
	}
	if machine != renewMachineID || end != beforeTTL.Add(90*24*time.Hour).UnixMilli() {
		t.Errorf("cert_serials cd01 = %q %d", machine, end)
	}
	var serials, renewals int
	if err := db.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM cert_serials), (SELECT count(*) FROM cert_renewals)`).
		Scan(&serials, &renewals); err != nil {
		t.Fatal(err)
	}
	if serials != 2 || renewals != 1 {
		t.Errorf("%d serials and %d renewals, want 2 and 1", serials, renewals)
	}
}

// Every refusal is decided before Sign runs and writes nothing.
func TestRenewRefusesWithoutALiveMachineAndAKnownSerial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		setup string
		edit  func(*statedb.Renewal)
		want  error
	}{
		{"unknown machine", "", func(r *statedb.Renewal) { r.MachineID = "01jmachine0000000000000009" }, statedb.ErrMachineUnknown},
		{"deleted machine", `DELETE FROM enroll_responses; DELETE FROM machines`, nil, statedb.ErrMachineUnknown},
		{"revoked machine", `UPDATE machines SET state = 'REVOKED'`, nil, statedb.ErrMachineRevoked},
		{"unknown serial", "", func(r *statedb.Renewal) { r.PeerSerial = "ff99" }, statedb.ErrPeerSerialUnknown},
		{"revoked serial", `UPDATE cert_serials SET revoked_time = 1`, nil, statedb.ErrPeerSerialUnknown},
		{"another machine's serial", `UPDATE cert_serials SET machine_id = 'other'`, nil, statedb.ErrPeerSerialUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := enrolledStore(t)
			if tc.setup != "" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := rawDB(t, dir).ExecContext(t.Context(), tc.setup); err != nil {
					t.Fatal(err)
				}
				s = open(t, dir)
			}
			var signs atomic.Int32
			r := renewal(2, "cd01", &signs)
			if tc.edit != nil {
				tc.edit(&r)
			}
			if _, err := statedb.Renew(context.Background(), s, r); !errors.Is(err, tc.want) {
				t.Errorf("Renew = %v, want %v", err, tc.want)
			}
			if signs.Load() != 0 {
				t.Error("Sign ran for a refused renewal")
			}
		})
	}
}

// Concurrent renewals for one machine and key sign once and store one
// serial; every caller gets that certificate.
func TestConcurrentRenewalsStoreOneSerial(t *testing.T) {
	t.Parallel()
	_, s := enrolledStore(t)
	const n = 8
	var signs atomic.Int32
	results := make([]statedb.Renewed, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			results[i], errs[i] = statedb.Renew(context.Background(), s, renewal(2, fmt.Sprintf("c%d", i), &signs))
		})
	}
	wg.Wait()
	fresh := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("Renew %d: %v", i, errs[i])
		}
		if results[i].Serial != results[0].Serial {
			t.Errorf("caller %d got serial %s, caller 0 %s", i, results[i].Serial, results[0].Serial)
		}
		if !results[i].Replayed {
			fresh++
		}
	}
	if fresh != 1 || signs.Load() != 1 {
		t.Errorf("%d fresh answers and %d signatures, want 1 and 1", fresh, signs.Load())
	}
}

func TestCertRenewalsConstraintsOnTheFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	// foreign_keys is per connection; the store turns it on for its own.
	db, err := sql.Open("sqlite", statedb.Path(dir)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	ins := `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES (?, 'm1', 1, 1)`
	for _, serial := range []string{"a1", "b2"} {
		if _, err := db.ExecContext(ctx, ins, serial); err != nil {
			t.Fatal(err)
		}
	}
	h := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	insRenewal := `INSERT INTO cert_renewals(machine_id, csr_key_hash, serial, cert, created_time) VALUES (?, ?, ?, x'00', 1)`
	if _, err := db.ExecContext(ctx, insRenewal, "m1", h(1), "a1"); err != nil {
		t.Fatal(err)
	}
	refused := map[string]func() error{
		"second certificate for one machine and key": func() error { _, err := db.ExecContext(ctx, insRenewal, "m1", h(1), "b2"); return err },
		"one serial for two keys":                    func() error { _, err := db.ExecContext(ctx, insRenewal, "m1", h(2), "a1"); return err },
		"serial not in cert_serials":                 func() error { _, err := db.ExecContext(ctx, insRenewal, "m1", h(3), "zz"); return err },
		"short key hash":                             func() error { _, err := db.ExecContext(ctx, insRenewal, "m1", []byte{1}, "b2"); return err },
		"empty machine id":                           func() error { _, err := db.ExecContext(ctx, insRenewal, "", h(4), "b2"); return err },
	}
	for name, run := range refused {
		if err := run(); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: %v, want a constraint error", name, err)
		}
	}
}

// A state.db from plan row 3 (migrations 1 to 3) gains cert_renewals.
func TestOpenMigratesAStoreFromTheMachinesSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:3]})
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
	if got := s.SchemaVersion(); got != 4 {
		t.Errorf("schema version %d, want 4", got)
	}
	if err := statedb.InsertEnrollToken(context.Background(), s, sampleToken("local:a", "r", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Enroll(context.Background(), s, newMachine(enrollKey("i", 1), renewMachineID, beforeTTL)); err != nil {
		t.Fatal(err)
	}
	var signs atomic.Int32
	if _, err := statedb.Renew(context.Background(), s, renewal(2, "cd01", &signs)); err != nil {
		t.Errorf("Renew after migration: %v", err)
	}
}
