package statedb_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Two first enrolls with one token both pass the read pre-check; the writer
// job decides. The same triple gets the winner's response as a replay, a
// different one ENROLL_TOKEN_USED, and there is exactly one machine row.
func TestConcurrentFirstEnrollsMakeOneMachine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		second   statedb.EnrollKey
		wantErr  error
		wantSame bool
	}{
		{"same install and key", enrollKey("install-1", 1), nil, true},
		{"another install", enrollKey("install-2", 1), statedb.ErrTokenUsed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := tokenStore(t)
			ctx := context.Background()
			keys := []statedb.EnrollKey{enrollKey("install-1", 1), tc.second}
			for _, k := range keys {
				if _, err := statedb.CheckEnroll(ctx, s, k, beforeTTL); err != nil {
					t.Fatalf("pre-check: %v", err)
				}
			}
			ids := []string{"01jmachine0000000000000001", "01jmachine0000000000000002"}
			results := make([]statedb.Enrolled, 2)
			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Go(func() { results[i], errs[i] = statedb.Enroll(ctx, s, newMachine(keys[i], ids[i], beforeTTL)) })
			}
			wg.Wait()
			won := 0
			if errs[0] != nil || results[0].Replayed {
				won = 1
			}
			lost := 1 - won
			if errs[won] != nil || results[won].Replayed {
				t.Fatalf("no first enroll won: %v %+v / %v %+v", errs[0], results[0], errs[1], results[1])
			}
			if !errors.Is(errs[lost], tc.wantErr) {
				t.Fatalf("the other enroll: %v, want %v", errs[lost], tc.wantErr)
			}
			if tc.wantSame && (!results[lost].Replayed || results[lost].MachineID != results[won].MachineID ||
				!bytes.Equal(results[lost].CertDER, results[won].CertDER)) {
				t.Errorf("the other enroll got %+v, want a replay of %+v", results[lost], results[won])
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"machines", "cert_serials", "enroll_responses", "audit_log WHERE action = 'machine.enroll'"} {
				if n := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM "+q); n != 1 {
					t.Errorf("%d rows in %s, want 1", n, q)
				}
			}
		})
	}
}

// A token that expires between the pre-check and the writer job is not
// burned: the job's own check refuses it.
func TestTheWriterJobRechecksExpiry(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	key := enrollKey("install-1", 1)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(key, "01jmachine0000000000000001", notAfter)); !errors.Is(err, statedb.ErrTokenExpired) {
		t.Fatalf("Enroll at not_after = %v, want ErrTokenExpired", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"machines", "cert_serials", "enroll_responses"} {
		if n := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM "+q); n != 0 {
			t.Errorf("%d rows in %s, want 0", n, q)
		}
	}
}

func TestMachineTablesConstraintsOnTheFile(t *testing.T) {
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
	insMachine := `INSERT INTO machines(machine_id, name, expected_hostname, labels, drained, install_id, state,
		hostname_mismatch, facts, created_time) VALUES (?, 'm', 'm', '[]', 0, 'i', ?, 0, '{}', 1)`
	if _, err := db.ExecContext(ctx, insMachine, "m1", "ENROLLED"); err != nil {
		t.Fatal(err)
	}
	for _, st := range []string{"ONLINE", "STALE", "OFFLINE", "REVOKED"} {
		if _, err := db.ExecContext(ctx, insMachine, "m-"+st, st); err != nil {
			t.Errorf("state %s refused: %v", st, err)
		}
	}
	h := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	insResp := `INSERT INTO enroll_responses(secret_hash, install_id, csr_key_hash, machine_id, cert, expire_time, created_time)
		VALUES (?, ?, ?, ?, x'00', 1, 1)`
	if _, err := db.ExecContext(ctx, insResp, h(1), "i", h(2), "m1"); err != nil {
		t.Fatal(err)
	}
	insSerial := `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES (?, 'm1', 1, 1)`
	if _, err := db.ExecContext(ctx, insSerial, "ab"); err != nil {
		t.Fatal(err)
	}
	refused := map[string]func() error{
		"unknown machine state": func() error { _, err := db.ExecContext(ctx, insMachine, "m2", "BOGUS"); return err },
		"duplicate machine_id":  func() error { _, err := db.ExecContext(ctx, insMachine, "m1", "ENROLLED"); return err },
		"second response for one secret": func() error {
			_, err := db.ExecContext(ctx, insResp, h(1), "j", h(3), "m1")
			return err
		},
		"response for no machine": func() error { _, err := db.ExecContext(ctx, insResp, h(4), "i", h(2), "nope"); return err },
		"duplicate serial":        func() error { _, err := db.ExecContext(ctx, insSerial, "ab"); return err },
		"uppercase serial":        func() error { _, err := db.ExecContext(ctx, insSerial, "CD"); return err },
	}
	for name, run := range refused {
		if err := run(); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: %v, want a constraint error", name, err)
		}
	}
}

// A state.db from plan row 2b (migrations 1 and 2) gains the row 3 tables.
func TestOpenMigratesAStoreFromTheEnrollTokenSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:2]})
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
	if got := s.SchemaVersion(); got != len(statedb.Migrations()) {
		t.Errorf("schema version %d, want %d", got, len(statedb.Migrations()))
	}
	if err := statedb.InsertEnrollToken(context.Background(), s, sampleToken("local:a", "r", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Enroll(context.Background(), s, newMachine(enrollKey("i", 1), "01jmachine0000000000000001", beforeTTL)); err != nil {
		t.Errorf("Enroll after migration: %v", err)
	}
}

func TestRevokeSerialsAndTheLiveRevokedList(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db := rawDB(t, dir)
	now := time.UnixMilli(1_790_000_000_000)
	ins := `INSERT INTO cert_serials(serial, machine_id, not_after_time, created_time) VALUES (?, 'm1', ?, 1)`
	for serial, end := range map[string]time.Time{"a1": now.Add(time.Hour), "b2": now.Add(-time.Hour), "c3": now.Add(time.Hour)} {
		if _, err := db.ExecContext(t.Context(), ins, serial, end.UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	s := open(t, dir)
	ctx := context.Background()
	n, err := statedb.RevokeSerials(ctx, s, []string{"a1", "b2", "zz"}, now)
	if err != nil || n != 2 {
		t.Fatalf("RevokeSerials = %d, %v; want 2 (zz is unknown)", n, err)
	}
	if n, err := statedb.RevokeSerials(ctx, s, []string{"a1"}, now.Add(time.Minute)); err != nil || n != 0 {
		t.Errorf("revoking again = %d, %v; want 0", n, err)
	}
	got, err := statedb.RevokedSerials(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []statedb.RevokedSerial{{Serial: "a1", NotAfter: now.Add(time.Hour)}}
	if len(got) != 1 || got[0].Serial != want[0].Serial || !got[0].NotAfter.Equal(want[0].NotAfter) {
		t.Errorf("RevokedSerials = %+v, want %+v (b2 has expired, c3 is live)", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var revoked int64
	if err := db.QueryRowContext(t.Context(), `SELECT revoked_time FROM cert_serials WHERE serial = 'a1'`).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked != now.UnixMilli() {
		t.Errorf("a1 revoked_time %d, want the first revoke's %d", revoked, now.UnixMilli())
	}
}
