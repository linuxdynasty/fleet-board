package statedb_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// The sample token (sampleToken body 1) is valid until notAfter.
var (
	notAfter  = time.UnixMilli(1_790_000_900_000)
	beforeTTL = notAfter.Add(-10 * time.Minute)
)

func enrollKey(install string, key byte) statedb.EnrollKey {
	return statedb.EnrollKey{
		SecretHash: sha256.Sum256([]byte{1, 1, 2, 3}),
		InstallID:  install,
		CSRKeyHash: sha256.Sum256([]byte{key}),
	}
}

func newMachine(key statedb.EnrollKey, id string, at time.Time) statedb.NewMachine {
	return statedb.NewMachine{
		Key: key, MachineID: id, CertDER: []byte("cert-" + id), Serial: "ab" + id[len(id)-4:],
		CertNotAfter: at.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan", Facts: `{"hostname":"laptop.lan"}`, Now: at,
	}
}

// tokenStore is a fresh state.db holding the sample token.
func tokenStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	if err := statedb.InsertEnrollToken(context.Background(), s, sampleToken("local:alice", "req-1", 1)); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func TestEnrollBurnsTheTokenAndWritesMachineSerialAndResponse(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	key := enrollKey("install-1", 1)
	got, err := statedb.Enroll(context.Background(), s, newMachine(key, "01jmachine0000000000000001", beforeTTL))
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	want := statedb.Enrolled{
		MachineID: "01jmachine0000000000000001", CertDER: []byte("cert-01jmachine0000000000000001"),
		MachineName: "laptop", ExpectedHostname: "laptop.lan",
	}
	if !equalEnrolled(got, want) {
		t.Errorf("Enroll = %+v, want %+v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	var (
		name, host, labels, install, state, facts string
		drained, mismatch                         int
		created                                   int64
	)
	err = db.QueryRowContext(t.Context(), `SELECT name, expected_hostname, labels, drained, install_id, state,
		hostname_mismatch, facts, created_time FROM machines WHERE machine_id = '01jmachine0000000000000001'`).
		Scan(&name, &host, &labels, &drained, &install, &state, &mismatch, &facts, &created)
	if err != nil {
		t.Fatal(err)
	}
	if name != "laptop" || host != "laptop.lan" || labels != `["gpu","home"]` || drained != 1 || install != "install-1" ||
		state != "ENROLLED" || mismatch != 0 || facts != `{"hostname":"laptop.lan"}` || created != beforeTTL.UnixMilli() {
		t.Errorf("machine row %q %q %q drained=%d %q %q mismatch=%d %q %d", name, host, labels, drained, install, state, mismatch, facts, created)
	}
	var serialMachine string
	var serialNotAfter int64
	var revoked sql.NullInt64
	if err := db.QueryRowContext(t.Context(), `SELECT machine_id, not_after_time, revoked_time FROM cert_serials WHERE serial = 'ab0001'`).
		Scan(&serialMachine, &serialNotAfter, &revoked); err != nil {
		t.Fatal(err)
	}
	if serialMachine != "01jmachine0000000000000001" || serialNotAfter != beforeTTL.Add(90*24*time.Hour).UnixMilli() || revoked.Valid {
		t.Errorf("cert_serials row %q %d revoked=%v", serialMachine, serialNotAfter, revoked)
	}
	var respMachine string
	var cert []byte
	var expire int64
	if err := db.QueryRowContext(t.Context(), `SELECT machine_id, cert, expire_time FROM enroll_responses
		WHERE secret_hash = ? AND install_id = 'install-1' AND csr_key_hash = ?`, key.SecretHash[:], key.CSRKeyHash[:]).
		Scan(&respMachine, &cert, &expire); err != nil {
		t.Fatal(err)
	}
	if respMachine != "01jmachine0000000000000001" || string(cert) != "cert-01jmachine0000000000000001" ||
		expire != notAfter.Add(24*time.Hour).UnixMilli() {
		t.Errorf("enroll_responses row %q %q expire %d, want the token's not_after + 24 h", respMachine, cert, expire)
	}
	var used sql.NullInt64
	if err := db.QueryRowContext(t.Context(), `SELECT used_time FROM enroll_tokens`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if !used.Valid || used.Int64 != beforeTTL.UnixMilli() {
		t.Errorf("used_time %v, want %d", used, beforeTTL.UnixMilli())
	}
}

func equalEnrolled(a, b statedb.Enrolled) bool {
	return a.MachineID == b.MachineID && bytes.Equal(a.CertDER, b.CertDER) && a.MachineName == b.MachineName &&
		a.ExpectedHostname == b.ExpectedHostname && a.HostnameMismatch == b.HostnameMismatch && a.Replayed == b.Replayed
}

func rawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 02g check order: unknown token, then (burned) the replay triple, then
// expiry; a replay after the TTL works until the TTL + 24 h.
func TestCheckEnrollOrder(t *testing.T) {
	t.Parallel()
	first := enrollKey("install-1", 1)
	other := statedb.EnrollKey{SecretHash: sha256.Sum256([]byte("nope")), InstallID: "install-1", CSRKeyHash: first.CSRKeyHash}
	cases := []struct {
		name   string
		enroll bool // enroll first with key "first"
		key    statedb.EnrollKey
		at     time.Time
		want   error
		replay bool
	}{
		{"unknown token", false, other, beforeTTL, statedb.ErrTokenUnknown, false},
		{"fresh token", false, first, beforeTTL, nil, false},
		{"unburned past its TTL", false, first, notAfter, statedb.ErrTokenExpired, false},
		{"same triple replays", true, first, beforeTTL, nil, true},
		{"same triple after the TTL replays", true, first, notAfter.Add(23 * time.Hour), nil, true},
		{"same triple past TTL + 24 h is used", true, first, notAfter.Add(24 * time.Hour), statedb.ErrTokenUsed, false},
		{"another install_id is used", true, enrollKey("install-2", 1), beforeTTL, statedb.ErrTokenUsed, false},
		{"another CSR key is used", true, enrollKey("install-1", 2), beforeTTL, statedb.ErrTokenUsed, false},
		{"unknown beats used", true, other, beforeTTL, statedb.ErrTokenUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, s := tokenStore(t)
			ctx := context.Background()
			if tc.enroll {
				if _, err := statedb.Enroll(ctx, s, newMachine(first, "01jmachine0000000000000001", beforeTTL)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := statedb.CheckEnroll(ctx, s, tc.key, tc.at)
			if !errors.Is(err, tc.want) {
				t.Fatalf("CheckEnroll = %v, want %v", err, tc.want)
			}
			if err != nil {
				return
			}
			if (got.Replay != nil) != tc.replay {
				t.Fatalf("Replay = %+v, want replay %v", got.Replay, tc.replay)
			}
			if tc.replay {
				want := statedb.Enrolled{
					MachineID: "01jmachine0000000000000001", CertDER: []byte("cert-01jmachine0000000000000001"),
					MachineName: "laptop", ExpectedHostname: "laptop.lan", Replayed: true,
				}
				if !equalEnrolled(*got.Replay, want) {
					t.Errorf("Replay = %+v, want %+v", *got.Replay, want)
				}
				return
			}
			if got.Token.MachineName != "laptop" || !got.Token.NotAfter.Equal(notAfter) {
				t.Errorf("Token = %+v, want the sample token", got.Token)
			}
		})
	}
}

// A stored response is never replayed for a machine that is not live.
func TestAReplayForARevokedMachineIsUsed(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	key := enrollKey("install-1", 1)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(key, "01jmachine0000000000000001", beforeTTL)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE machines SET state = 'REVOKED'`); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	if _, err := statedb.CheckEnroll(context.Background(), s, key, beforeTTL); !errors.Is(err, statedb.ErrTokenUsed) {
		t.Errorf("CheckEnroll = %v, want ErrTokenUsed", err)
	}
}

// Row 3 does not re-enroll (13a): a token naming a previous machine is
// never burned.
func TestATokenWithAPreviousMachineIsNotBurned(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `UPDATE enroll_tokens SET previous_machine = '01jold'`); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	key := enrollKey("install-1", 1)
	if _, err := statedb.CheckEnroll(context.Background(), s, key, beforeTTL); !errors.Is(err, statedb.ErrPreviousMachine) {
		t.Errorf("CheckEnroll = %v, want ErrPreviousMachine", err)
	}
	if _, err := statedb.Enroll(context.Background(), s, newMachine(key, "01jmachine0000000000000001", beforeTTL)); !errors.Is(err, statedb.ErrPreviousMachine) {
		t.Errorf("Enroll = %v, want ErrPreviousMachine", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM enroll_tokens WHERE used_time IS NOT NULL"); n != 0 {
		t.Error("the token was burned")
	}
}

func TestHostnameMismatchIsSetOnTheRow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, expected, reported string
		mismatch                 bool
	}{
		{"same", "laptop.lan", "laptop.lan", false},
		{"case and trailing dot", "laptop.lan", "LAPTOP.LAN.", false},
		{".local is dropped on both sides", "studio", "Studio.local", false},
		{"empty expected means the name", "", "laptop", false},
		{"different", "laptop.lan", "desktop", true},
		{"empty reported", "laptop.lan", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			create(t, dir)
			s := open(t, dir)
			tok := sampleToken("local:alice", "req-1", 1)
			tok.ExpectedHostname = tc.expected
			ctx := context.Background()
			if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
				t.Fatal(err)
			}
			m := newMachine(enrollKey("install-1", 1), "01jmachine0000000000000001", beforeTTL)
			m.ReportedHostname = tc.reported
			got, err := statedb.Enroll(ctx, s, m)
			if err != nil {
				t.Fatal(err)
			}
			if got.HostnameMismatch != tc.mismatch {
				t.Errorf("HostnameMismatch = %v, want %v", got.HostnameMismatch, tc.mismatch)
			}
			wantHost := tc.expected
			if wantHost == "" {
				wantHost = "laptop"
			}
			if got.ExpectedHostname != wantHost {
				t.Errorf("ExpectedHostname = %q, want %q", got.ExpectedHostname, wantHost)
			}
		})
	}
}
