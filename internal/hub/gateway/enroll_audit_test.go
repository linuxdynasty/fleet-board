package gateway_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// auditRow is one audit_log row read back through the store.
type auditRow struct {
	action, target, machine, outcome, reason string
	detail                                   map[string]any
	occurred                                 sql.NullInt64
	whole                                    string
}

func (h *hub) auditRows(t *testing.T, action string) []auditRow {
	t.Helper()
	var out []auditRow
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT action, target, machine_id, outcome, reason, detail,
			occurred_time, actor_id || '|' || actor_kind || '|' || actor_name || '|' || target || '|' || detail || '|' || reason
			FROM audit_log WHERE action = ? ORDER BY id`, action)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r auditRow
			var detail string
			if err := rows.Scan(&r.action, &r.target, &r.machine, &r.outcome, &r.reason, &detail, &r.occurred, &r.whole); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(detail), &r.detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A refused Enroll writes one machine.enroll_failed row with the reason, the
// gRPC code and the peer's address without its port; never the token.
func TestARefusedEnrollWritesOneAuditRow(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	tok := h.token(t, h.clock.now().Add(-time.Second)) // expired
	if _, err := h.enroll(t, enrollRequest(tok, newAgentKey(t), "install-1")); err == nil {
		t.Fatal("an expired token enrolled")
	}
	rows := h.auditRows(t, "machine.enroll_failed")
	if len(rows) != 1 {
		t.Fatalf("%d machine.enroll_failed rows, want 1", len(rows))
	}
	r := rows[0]
	if r.target != "machines" || r.machine != "" || r.outcome != "FAILED" || r.reason != "ENROLL_TOKEN_EXPIRED" ||
		r.detail["peer"] != "127.0.0.1" || r.detail["code"] != "unauthenticated" || r.occurred.Valid {
		t.Errorf("row = %+v", r)
	}
	if _, ok := r.detail["suppressed"]; ok {
		t.Errorf("first row carries suppressed: %v", r.detail)
	}
	if strings.Contains(r.whole, tok) || strings.Contains(r.whole, strings.TrimPrefix(tok, "fbe1_")[:16]) {
		t.Errorf("the row holds the token: %q", r.whole)
	}
}

// Four refusals of one pair over two minutes: one row, then (next minute)
// the summary of the first and a row carrying suppressed=2. The refused
// counter still counts all four.
func TestRefusalsAreCappedPerPeerAndSummarizedNextMinute(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	start := h.clock.now().Truncate(time.Minute)
	refuse := func(at time.Time) {
		t.Helper()
		h.clock.set(at)
		if _, err := h.enroll(t, enrollRequest("fbe1_nope", newAgentKey(t), "install-1")); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("enroll: %v, want unauthenticated", err)
		}
	}
	for i := range 3 {
		refuse(start.Add(time.Duration(10+i) * time.Second))
	}
	if n := len(h.auditRows(t, "machine.enroll_failed")); n != 1 {
		t.Fatalf("%d rows in the first minute, want 1", n)
	}
	refuse(start.Add(time.Minute + 5*time.Second))
	rows := h.auditRows(t, "machine.enroll_failed")
	if len(rows) != 3 {
		t.Fatalf("%d rows, want 3 (row, summary, row)", len(rows))
	}
	sum, next := rows[1], rows[2]
	want := map[string]any{
		"window_start": start.UTC().Format(time.RFC3339), "suppressed": float64(2),
		"distinct_peers": float64(1), "reasons": map[string]any{"ENROLL_TOKEN_UNKNOWN": float64(2)},
	}
	if g, w := mustJSON(t, sum.detail), mustJSON(t, want); g != w || sum.reason != "" || sum.target != "machines" ||
		!sum.occurred.Valid || sum.occurred.Int64 != start.UnixMilli() {
		t.Errorf("summary = %+v (detail %s), want detail %s occurred at the window start", sum, g, w)
	}
	if next.detail["suppressed"] != float64(2) || next.reason != "ENROLL_TOKEN_UNKNOWN" {
		t.Errorf("next row = %+v, want suppressed=2", next)
	}
	body := scrape(t, h.fixture)
	for result, want := range map[string]float64{"written": 2, "summary": 1, "suppressed": 2} {
		if n := sample(body, "fleet_hub_enroll_audit_rows_total", `result="`+result+`"`); n != want {
			t.Errorf("fleet_hub_enroll_audit_rows_total{result=%s} = %v, want %v", result, n, want)
		}
	}
	if n := sample(body, "fleet_hub_enroll_total", `result="refused"`); n != 4 {
		t.Errorf("fleet_hub_enroll_total{result=refused} = %v, want 4", n)
	}
}

// FlushAudit, called after Shutdown, writes the summary of the minute in
// progress before it returns; a second call writes nothing.
func TestFlushAuditWritesTheMinuteInProgress(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	for range 2 {
		if _, err := h.enroll(t, enrollRequest("fbe1_nope", newAgentKey(t), "install-1")); err == nil {
			t.Fatal("enrolled")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := h.auditRows(t, "machine.enroll_failed"); len(rows) != 1 {
		t.Fatalf("%d rows after Shutdown alone, want 1 (no flush before FlushAudit)", len(rows))
	}
	h.srv.FlushAudit(ctx)
	h.srv.FlushAudit(ctx)
	rows := h.auditRows(t, "machine.enroll_failed")
	if len(rows) != 2 || rows[1].detail["suppressed"] != float64(1) {
		t.Fatalf("rows after FlushAudit = %+v, want the row and one summary with suppressed=1", rows)
	}
}

// failAuditInserts makes every audit_log insert in the hub's file fail.
func (h *hub) failAuditInserts(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", statedb.Path(h.dir))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
}

// When the store does not take a refusal's row, the agent still gets the
// same refusal, and the failure is counted and logged.
func TestARefusalWhoseAuditRowFailsIsStillRefusedAndSaysSo(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	h.failAuditInserts(t)
	_, err := h.enroll(t, enrollRequest("fbe1_nope", newAgentKey(t), "install-1"))
	if got := refusalOf(t, err); got.code != connect.CodeUnauthenticated || got.reason != typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_UNKNOWN {
		t.Fatalf("refusal %v / %v, want unauthenticated / ENROLL_TOKEN_UNKNOWN as without the failure", got.code, got.reason)
	}
	if n := sample(scrape(t, h.fixture), "fleet_hub_enroll_audit_rows_total", `result="write_failed"`); n != 1 {
		t.Errorf("fleet_hub_enroll_audit_rows_total{result=write_failed} = %v, want 1", n)
	}
	lines := h.events(t, "enroll.audit_failed")
	if len(lines) != 1 || lines[0]["rows"] != float64(1) || lines[0]["err_kind"] == nil || lines[0]["level"] != "error" {
		t.Errorf("enroll.audit_failed lines %v, want one error with rows=1 and err_kind", lines)
	}
}

// The row is written even when the agent's deadline ends while the hub
// waits for the commit: the wait is not tied to the request's context.
func TestARefusalRowOutlivesTheAgentsDeadline(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	client := h.client(t)
	if _, err := client.Enroll(context.Background(), connect.NewRequest(enrollRequest("fbe1_nope", newAgentKey(t), "install-1"))); err == nil {
		t.Fatal("enrolled") // also opens the connection, so the next call is sent at once
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond) // under the 250 ms batch window
	defer cancel()
	_, _ = client.Enroll(ctx, connect.NewRequest(enrollRequest("fbe1_nope", newAgentKey(t), "")))
	deadline := time.Now().Add(10 * time.Second)
	for len(h.auditRows(t, "machine.enroll_failed")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the second refusal's row was never written")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := sample(scrape(t, h.fixture), "fleet_hub_enroll_audit_rows_total", `result="write_failed"`); n > 0 {
		t.Errorf("write_failed = %v, want none", n)
	}
}

// A first Enroll writes one machine.enroll row; its replay writes none.
func TestAnEnrollWritesOneRowAndItsReplayNone(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
	res := h.mustEnroll(t, req)
	if again := h.mustEnroll(t, req); !again.GetReplayed() {
		t.Fatal("the second Enroll was not a replay")
	}
	rows := h.auditRows(t, "machine.enroll")
	id := res.GetMachineId().GetValue()
	if len(rows) != 1 || rows[0].target != "machines/"+id || rows[0].machine != id {
		t.Errorf("machine.enroll rows = %+v, want one for %s", rows, id)
	}
	if n := len(h.auditRows(t, "machine.enroll_failed")); n != 0 {
		t.Errorf("%d machine.enroll_failed rows after a success", n)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
