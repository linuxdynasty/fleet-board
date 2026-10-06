package statedb_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func sampleToken(principal, requestID string, body byte) statedb.EnrollToken {
	return statedb.EnrollToken{
		SecretHash:       sha256.Sum256([]byte{body, 1, 2, 3}),
		MachineName:      "laptop",
		ExpectedHostname: "laptop.lan",
		Labels:           []string{"gpu", "home"},
		StartDrained:     true,
		NotAfter:         time.UnixMilli(1_790_000_900_000),
		Created:          time.UnixMilli(1_790_000_000_000),
		Principal:        principal,
		RequestID:        requestID,
		RequestHash:      sha256.Sum256([]byte{body}),
	}
}

func TestAnEnrollTokenRowHoldsTheHashAndTheRequest(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	tok := sampleToken("local:alice", "req-1", 1)
	if err := statedb.InsertEnrollToken(context.Background(), s, tok); err != nil {
		t.Fatalf("InsertEnrollToken: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", statedb.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var (
		hash, reqHash                    []byte
		name, host, labels, princ, reqID string
		drained                          int
		notAfter, created                int64
		used                             sql.NullInt64
		prev                             sql.NullString
	)
	err = db.QueryRowContext(t.Context(), `SELECT secret_hash, machine_name, expected_hostname, labels, start_drained, not_after_time,
		used_time, previous_machine, created_time, principal, request_id, request_hash FROM enroll_tokens`).
		Scan(&hash, &name, &host, &labels, &drained, &notAfter, &used, &prev, &created, &princ, &reqID, &reqHash)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte{1, 1, 2, 3})
	wantReq := sha256.Sum256([]byte{1})
	if !bytes.Equal(hash, wantHash[:]) || !bytes.Equal(reqHash, wantReq[:]) {
		t.Error("secret_hash or request_hash differ from what was inserted")
	}
	if name != "laptop" || host != "laptop.lan" || labels != `["gpu","home"]` || drained != 1 {
		t.Errorf("row = %q %q %q drained=%d", name, host, labels, drained)
	}
	if notAfter != 1_790_000_900_000 || created != 1_790_000_000_000 || used.Valid || prev.Valid {
		t.Errorf("times %d %d, used %v, previous_machine %v", notAfter, created, used, prev)
	}
	if princ != "local:alice" || reqID != "req-1" {
		t.Errorf("principal %q request_id %q", princ, reqID)
	}
}

func TestEnrollTokenRequestIDRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		second statedb.EnrollToken
		want   error
	}{
		{"same principal, id and body", sampleToken("local:alice", "req-1", 1), statedb.ErrSecretDelivered},
		{"same principal and id, other body", sampleToken("local:alice", "req-1", 2), statedb.ErrRequestConflict},
		{"other principal, same id", sampleToken("local:bob", "req-1", 3), nil},
		{"same principal, other id", sampleToken("local:alice", "req-2", 4), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			create(t, dir)
			s := open(t, dir)
			ctx := context.Background()
			if err := statedb.InsertEnrollToken(ctx, s, sampleToken("local:alice", "req-1", 1)); err != nil {
				t.Fatal(err)
			}
			err := statedb.InsertEnrollToken(ctx, s, tc.second)
			if !errors.Is(err, tc.want) {
				t.Fatalf("second insert: %v, want %v", err, tc.want)
			}
			wantRows := 2
			if tc.want != nil {
				wantRows = 1
			}
			if got := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM enroll_tokens"); got != wantRows {
				t.Errorf("%d rows, want %d", got, wantRows)
			}
		})
	}
}

// 2b re-enrolls nothing: any previous_machine is refused and stores no row.
// The request-id check comes first (02b precedence: ALREADY_EXISTS before
// FAILED_PRECONDITION).
func TestAPreviousMachineIsNotReenrollable(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	tok := sampleToken("local:alice", "req-1", 1)
	tok.PreviousMachine = "01jabcdefghjkmnpqrstvwxyz0"
	if err := statedb.InsertEnrollToken(ctx, s, tok); !errors.Is(err, statedb.ErrNotReenrollable) {
		t.Fatalf("insert with previous_machine: %v, want ErrNotReenrollable", err)
	}
	if got := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM enroll_tokens"); got != 0 {
		t.Errorf("%d rows stored, want 0", got)
	}
	if err := statedb.InsertEnrollToken(ctx, s, sampleToken("local:alice", "req-2", 2)); err != nil {
		t.Fatal(err)
	}
	conflict := sampleToken("local:alice", "req-2", 9)
	conflict.PreviousMachine = "01jabcdefghjkmnpqrstvwxyz0"
	if err := statedb.InsertEnrollToken(ctx, s, conflict); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("reused id with previous_machine: %v, want ErrRequestConflict", err)
	}
}

// The unique constraints back the rules up on the file itself.
func TestEnrollTokenConstraintsOnTheFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db, err := sql.Open("sqlite", statedb.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ins := `INSERT INTO enroll_tokens(secret_hash, machine_name, expected_hostname, labels, start_drained,
		not_after_time, created_time, principal, request_id, request_hash) VALUES (?, 'm', '', '[]', 0, 1, 1, ?, ?, ?)`
	h := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	if _, err := db.ExecContext(t.Context(), ins, h(1), "local:a", "r1", h(9)); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]any{
		"duplicate secret_hash":          {h(1), "local:a", "r2", h(9)},
		"duplicate (principal, request)": {h(2), "local:a", "r1", h(9)},
		"short secret_hash":              {[]byte{1}, "local:a", "r3", h(9)},
		"empty request_id":               {h(3), "local:a", "", h(9)},
	} {
		if _, err := db.ExecContext(t.Context(), ins, args...); err == nil || !strings.Contains(err.Error(), "constraint") {
			t.Errorf("%s: %v, want a constraint error", name, err)
		}
	}
}

// A state.db made before enroll_tokens existed gains the table on Open.
func TestOpenMigratesAStoreFromTheIdentityOnlySchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	path := filepath.Join(dir, statedb.FileName)
	s, err := store.Open(context.Background(), store.Options{Path: path, Name: "state", Migrations: statedb.Migrations()[:1]})
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
		t.Errorf("insert after migration: %v", err)
	}
}
