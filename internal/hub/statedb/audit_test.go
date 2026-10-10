package statedb_test

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// auditRow is one audit_log row as stored in the file.
type auditRow struct {
	ID, HubSeq, Created              int64
	ActorID, ActorKind, ActorName    string
	Action, Target, Machine, Request string
	Goal, Command                    string
	Outcome, Reason, Detail          string
	Occurred                         sql.NullInt64
	Whole                            string // every column as text, joined
	detailMap                        map[string]any
}

// auditRows reads every audit_log row from the file, in id order.
func auditRows(t *testing.T, dir string) []auditRow {
	t.Helper()
	rows, err := rawDB(t, dir).QueryContext(t.Context(), `SELECT id, hub_seq, create_time, actor_id, actor_kind, actor_name,
		action, target, machine_id, goal_id, command_id, request_id, outcome, reason, detail, occurred_time,
		id || '|' || hub_seq || '|' || create_time || '|' || actor_id || '|' || actor_kind || '|' || actor_name || '|' ||
		action || '|' || target || '|' || machine_id || '|' || goal_id || '|' || command_id || '|' || request_id || '|' ||
		outcome || '|' || reason || '|' || detail || '|' || ifnull(occurred_time, '')
		FROM audit_log ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.ID, &r.HubSeq, &r.Created, &r.ActorID, &r.ActorKind, &r.ActorName, &r.Action, &r.Target,
			&r.Machine, &r.Goal, &r.Command, &r.Request, &r.Outcome, &r.Reason, &r.Detail, &r.Occurred, &r.Whole); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.Detail), &r.detailMap); err != nil {
			t.Errorf("row %d detail %q is not a JSON object: %v", r.ID, r.Detail, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func hubSeq(t *testing.T, dir string) int64 {
	t.Helper()
	var n int64
	if err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT hub_seq FROM hub_counters WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The three store actions each write exactly one row in their own job, with
// the fields 02m names; a replay writes none; hub_seq rises per changed
// resource (the audit row and, for Enroll and Renew, the machine).
func TestEachActionWritesOneAuditRowAndAReplayWritesNone(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t) // machine.enroll_token
	ctx := context.Background()
	m := newMachine(enrollKey("install-1", 1), renewMachineID, beforeTTL)
	m.ReportedHostname = "other-host"
	if _, err := statedb.Enroll(ctx, s, m); err != nil {
		t.Fatal(err)
	}
	if got, err := statedb.Enroll(ctx, s, m); err != nil || !got.Replayed {
		t.Fatalf("repeat Enroll = %+v, %v; want a replay", got, err)
	}
	var signs atomic.Int32
	renewAt := beforeTTL.Add(time.Minute)
	r := renewal(2, "cd01", &signs)
	r.Now = renewAt
	for range 2 {
		if _, err := statedb.Renew(ctx, s, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, dir)
	if len(rows) != 3 {
		t.Fatalf("%d audit rows, want 3 (token, enroll, renewal): %+v", len(rows), rows)
	}
	tok, enr, ren := rows[0], rows[1], rows[2]
	if tok.Action != "machine.enroll_token" || tok.Target != "enroll_tokens/1" || tok.Machine != "" ||
		tok.ActorID != "local:alice" || tok.Request != "req-1" || tok.Outcome != "OK" || tok.Reason != "" ||
		tok.Created != time.UnixMilli(1_790_000_000_000).UnixMilli() || tok.Occurred.Valid {
		t.Errorf("token row = %+v", tok)
	}
	wantTok := map[string]any{"machine_name": "laptop", "expected_hostname": "laptop.lan", "not_after": "2026-09-21T14:28:20Z"}
	if !sameMap(tok.detailMap, wantTok) {
		t.Errorf("token detail = %v, want %v", tok.detailMap, wantTok)
	}
	if enr.Action != "machine.enroll" || enr.Target != "machines/"+renewMachineID || enr.Machine != renewMachineID ||
		enr.ActorID != "" || enr.ActorKind != "" || enr.Outcome != "OK" || enr.Created != beforeTTL.UnixMilli() {
		t.Errorf("enroll row = %+v", enr)
	}
	wantEnr := map[string]any{
		"machine_name": "laptop", "expected_hostname": "laptop.lan", "hostname_mismatch": true,
		"enroll_token": "enroll_tokens/1",
	}
	if !sameMap(enr.detailMap, wantEnr) {
		t.Errorf("enroll detail = %v, want %v", enr.detailMap, wantEnr)
	}
	if strings.Contains(enr.Whole, "other-host") {
		t.Errorf("the enroll row holds the agent's reported hostname: %q", enr.Whole)
	}
	if ren.Action != "machine.cert_renewed" || ren.Target != "machines/"+renewMachineID || ren.Machine != renewMachineID ||
		ren.Created != renewAt.UnixMilli() {
		t.Errorf("renewal row = %+v", ren)
	}
	wantRen := map[string]any{"serial": "cd01", "not_after": "2026-12-20T14:18:20Z"} // the renewal helper's Sign
	if !sameMap(ren.detailMap, wantRen) {
		t.Errorf("renewal detail = %v, want %v", ren.detailMap, wantRen)
	}
	// hub_seq: token row 1; enroll stamps the machine (2) then its row (3);
	// the renewal the machine (4) then its row (5).
	if tok.HubSeq != 1 || enr.HubSeq != 3 || ren.HubSeq != 5 || hubSeq(t, dir) != 5 {
		t.Errorf("hub_seq token %d enroll %d renewal %d counter %d, want 1 3 5 5", tok.HubSeq, enr.HubSeq, ren.HubSeq, hubSeq(t, dir))
	}
	var version, seq, observed int64
	if err := rawDB(t, dir).QueryRowContext(t.Context(), `SELECT version, hub_seq, observed_time FROM machines`).
		Scan(&version, &seq, &observed); err != nil {
		t.Fatal(err)
	}
	if version != 1 || seq != 4 || observed != renewAt.UnixMilli() {
		t.Errorf("machine version %d hub_seq %d observed_time %d, want 1 4 %d", version, seq, observed, renewAt.UnixMilli())
	}
}

func sameMap(got, want map[string]any) bool {
	g, gerr := json.Marshal(got)
	w, werr := json.Marshal(want)
	return gerr == nil && werr == nil && string(g) == string(w)
}

// The token row is written from the hash only; it never holds it either.
func TestTheEnrollTokenRowHoldsNoPartOfTheSecretHash(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	h := sampleToken("local:alice", "req-1", 1).SecretHash
	for _, r := range auditRows(t, dir) {
		if strings.Contains(strings.ToLower(r.Whole), hex.EncodeToString(h[:4])) {
			t.Errorf("audit row holds the secret hash: %q", r.Whole)
		}
	}
}

// failAuditInserts makes every audit_log insert fail, as a crash between the
// change and the commit would: nothing of the job may survive.
func failAuditInserts(t *testing.T, dir string) func() {
	t.Helper()
	db := rawDB(t, dir)
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.ExecContext(context.Background(), `DROP TRIGGER fail_audit`); err != nil {
			t.Fatal(err)
		}
	}
}

// Each action fails as a whole when its audit row cannot be written: no
// change, no row, and hub_seq rolled back with the job's savepoint. The next
// job then takes the next number.
func TestAJobThatFailsAfterTheChangeLeavesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name  string
		table string
		run   func(t *testing.T, dir string) error
	}{
		{"enroll token", "enroll_tokens", func(t *testing.T, dir string) error {
			t.Helper()
			s := open(t, dir)
			return statedb.InsertEnrollToken(ctx, s, sampleToken("local:bob", "req-9", 9))
		}},
		{"enroll", "machines", func(t *testing.T, dir string) error {
			t.Helper()
			s := open(t, dir)
			_, err := statedb.Enroll(ctx, s, newMachine(enrollKey("install-1", 1), renewMachineID, beforeTTL))
			return err
		}},
		{"renewal", "cert_renewals", func(t *testing.T, dir string) error {
			t.Helper()
			s := open(t, dir)
			var signs atomic.Int32
			_, err := statedb.Renew(ctx, s, renewal(2, "cd01", &signs))
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, s := tokenStore(t)
			if tc.table == "cert_renewals" {
				if _, err := statedb.Enroll(ctx, s, newMachine(enrollKey("install-1", 1), renewMachineID, beforeTTL)); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before, rowsBefore := hubSeq(t, dir), len(auditRows(t, dir))
			tableBefore := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM "+tc.table)
			restore := failAuditInserts(t, dir)
			if err := tc.run(t, dir); err == nil {
				t.Fatal("the job succeeded with a failing audit insert")
			}
			if got := hubSeq(t, dir); got != before {
				t.Errorf("hub_seq %d after the failed job, want %d", got, before)
			}
			if n := len(auditRows(t, dir)); n != rowsBefore {
				t.Errorf("%d audit rows after the failed job, want %d", n, rowsBefore)
			}
			if n := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM "+tc.table); n != tableBefore {
				t.Errorf("%d rows in %s after the failed job, want %d", n, tc.table, tableBefore)
			}
			if tc.table == "machines" {
				if n := rawCount(t, statedb.Path(dir), "SELECT count(*) FROM enroll_tokens WHERE used_time IS NOT NULL"); n != 0 {
					t.Error("the failed enroll burned the token")
				}
			}
			restore()
			if err := tc.run(t, dir); err != nil {
				t.Fatalf("the retry failed: %v", err)
			}
			rows := auditRows(t, dir)
			if last := rows[len(rows)-1]; last.HubSeq <= before {
				t.Errorf("the retry's row has hub_seq %d, want above %d", last.HubSeq, before)
			}
		})
	}
}

var errJob = errors.New("job failed")

// A job that stamps and appends, then fails, leaves the counter and the log
// as they were; hub_seq is strictly increasing across jobs that commit.
func TestAppendAuditRollsBackWithItsJob(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	ctx := context.Background()
	entry := statedb.AuditEntry{
		Action: "machine.enroll_failed", Target: "machines", Outcome: "FAILED",
		Reason: "ENROLL_TOKEN_UNKNOWN", Detail: `{"peer":"192.0.2.1"}`, Now: beforeTTL,
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := statedb.AppendAudit(ctx, tx, entry); err != nil {
			return err
		}
		return errJob
	})
	if !errors.Is(err, errJob) {
		t.Fatalf("Write = %v, want errJob", err)
	}
	if err := statedb.WriteAudit(ctx, s, entry, entry); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, dir)
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (the token's and two written)", len(rows))
	}
	for i, r := range rows {
		if r.HubSeq != int64(i+1) {
			t.Errorf("row %d hub_seq %d, want %d (the failed job's numbers were rolled back)", i, r.HubSeq, i+1)
		}
	}
	if rows[1].Reason != "ENROLL_TOKEN_UNKNOWN" || rows[1].Outcome != "FAILED" || rows[1].Target != "machines" {
		t.Errorf("written row = %+v", rows[1])
	}
}

// Rows are refused when they break the contract: an empty action or target,
// or a detail over 1 KiB. Nothing is written.
func TestAppendAuditRefusesABadEntry(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	ok := statedb.AuditEntry{Action: "machine.enroll_failed", Target: "machines", Now: beforeTTL}
	cases := []struct {
		name string
		edit func(*statedb.AuditEntry)
	}{
		{"no action", func(e *statedb.AuditEntry) { e.Action = "" }},
		{"no target", func(e *statedb.AuditEntry) { e.Target = "" }},
		{"long detail", func(e *statedb.AuditEntry) { e.Detail = strings.Repeat("x", 1025) }},
	}
	for _, tc := range cases {
		e := ok
		tc.edit(&e)
		if err := statedb.WriteAudit(context.Background(), s, e); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if n := len(auditRows(t, dir)); n != 1 {
		t.Errorf("%d rows, want only the token's", n)
	}
}

// The log is permanent: the file refuses an update or a delete, and the rows
// and the counter survive a restart.
func TestAuditRowsArePermanentAndSurviveARestart(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	for _, q := range []string{`UPDATE audit_log SET detail = '{}'`, `DELETE FROM audit_log`} {
		if _, err := db.ExecContext(t.Context(), q); err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
	for _, q := range []string{
		`INSERT INTO hub_counters(id, hub_seq) VALUES (2, 0)`,
		`UPDATE hub_counters SET hub_seq = -1`,
		`INSERT INTO audit_log(create_time, hub_seq, action, target) VALUES (0, 1, 'x.y', 'z')`, // hub_seq taken
	} {
		if _, err := db.ExecContext(t.Context(), q); err == nil {
			t.Errorf("%s was accepted", q)
		}
	}
	s = open(t, dir)
	if err := statedb.InsertEnrollToken(context.Background(), s, sampleToken("local:alice", "req-2", 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, dir)
	if len(rows) != 2 || rows[0].Target != "enroll_tokens/1" || rows[1].Target != "enroll_tokens/2" || rows[1].HubSeq != 2 {
		t.Errorf("after a restart the rows are %+v, want both tokens with hub_seq 1 and 2", rows)
	}
}
