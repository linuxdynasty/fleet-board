package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The largest names the API accepts (a 128-byte machine name of characters
// JSON may escape, a 253-byte expected hostname) still fit the 1 KiB detail,
// through both InsertEnrollToken and Enroll, and come back unchanged.
func TestTheLargestAcceptedNamesFitTheAuditDetail(t *testing.T) {
	t.Parallel()
	for _, ch := range []string{"&", "<", ">", `"`, `\`} {
		t.Run(ch, func(t *testing.T) {
			t.Parallel()
			dir := stateDir(t)
			create(t, dir)
			s := open(t, dir)
			name, host := strings.Repeat(ch, 128), strings.Repeat("h", 253)
			tok := sampleToken("local:alice", "big", 7)
			tok.MachineName, tok.ExpectedHostname = name, host
			ctx := context.Background()
			if err := statedb.InsertEnrollToken(ctx, s, tok); err != nil {
				t.Fatalf("InsertEnrollToken: %v", err)
			}
			key := statedb.EnrollKey{SecretHash: tok.SecretHash, InstallID: "i", CSRKeyHash: sha256.Sum256([]byte{1})}
			if _, err := statedb.Enroll(ctx, s, newMachine(key, renewMachineID, beforeTTL)); err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			rows := auditRows(t, dir)
			if len(rows) != 2 {
				t.Fatalf("%d rows, want 2", len(rows))
			}
			for _, r := range rows {
				var d struct {
					MachineName      string `json:"machine_name"`
					ExpectedHostname string `json:"expected_hostname"`
				}
				if err := json.Unmarshal([]byte(r.Detail), &d); err != nil || d.MachineName != name || d.ExpectedHostname != host {
					t.Errorf("%s detail (%d bytes) does not round-trip: %v", r.Action, len(r.Detail), err)
				}
			}
		})
	}
}

// AppendAudit itself refuses a bad entry with ErrAuditInvalid before it
// takes a hub_seq; the table's CHECK is only the backstop.
func TestAppendAuditRefusesBeforeTakingAHubSeq(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	ctx := context.Background()
	cases := []struct {
		name string
		e    statedb.AuditEntry
	}{
		{"detail over 1 KiB", statedb.AuditEntry{Action: "a.b", Target: "x", Detail: strings.Repeat("x", 1025), Now: beforeTTL}},
		{"no action", statedb.AuditEntry{Target: "x", Now: beforeTTL}},
		{"no target", statedb.AuditEntry{Action: "a.b", Now: beforeTTL}},
	}
	for _, tc := range cases {
		var got error
		err := s.Write(ctx, func(tx *sql.Tx) error {
			_, got = statedb.AppendAudit(ctx, tx, tc.e)
			// The job carries on: only AppendAudit's own check can have
			// refused, and nothing it did may remain.
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !errors.Is(got, statedb.ErrAuditInvalid) {
			t.Errorf("%s: AppendAudit = %v, want ErrAuditInvalid", tc.name, got)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := hubSeq(t, dir); n != 1 {
		t.Errorf("hub_seq %d, want 1 (only the token's)", n)
	}
}

// A zero time is refused: NextStamp (and so AppendAudit) takes no number.
func TestAZeroTimeIsRefusedWithoutTakingAHubSeq(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	ctx := context.Background()
	var stampErr, auditErr error
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, stampErr = statedb.NextStamp(ctx, tx, time.Time{})
		_, auditErr = statedb.AppendAudit(ctx, tx, statedb.AuditEntry{Action: "a.b", Target: "x"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(stampErr, statedb.ErrZeroTime) || !errors.Is(auditErr, statedb.ErrZeroTime) {
		t.Errorf("NextStamp %v, AppendAudit %v; want ErrZeroTime for both", stampErr, auditErr)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := hubSeq(t, dir); n != 1 {
		t.Errorf("hub_seq %d, want 1 (only the token's)", n)
	}
}

// Neither an existing audit row nor the counter can be overwritten through
// INSERT OR REPLACE, deleted, or (the counter) moved back.
func TestTheLogAndTheCounterRefuseReplaceAndRollback(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for _, q := range []string{
		`INSERT OR REPLACE INTO audit_log(id, create_time, hub_seq, action, target) VALUES (1, 0, 99, 'x.y', 'z')`,
		`REPLACE INTO audit_log(id, create_time, hub_seq, action, target) VALUES (5, 0, 1, 'x.y', 'z')`,
		`INSERT OR REPLACE INTO hub_counters(id, hub_seq) VALUES (1, 0)`,
		`UPDATE hub_counters SET hub_seq = 0`,
		`DELETE FROM hub_counters`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
	rows := auditRows(t, dir)
	if len(rows) != 1 || rows[0].Action != "machine.enroll_token" || rows[0].HubSeq != 1 || hubSeq(t, dir) != 1 {
		t.Errorf("after the attempts: rows %+v, counter %d; want the token's row and 1", rows, hubSeq(t, dir))
	}
}
