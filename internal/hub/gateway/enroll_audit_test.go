package gateway_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
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

// Stopping the server writes the summary of the minute in progress.
func TestShutdownWritesTheMinuteInProgress(t *testing.T) {
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
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows := h.auditRows(t, "machine.enroll_failed")
		if len(rows) == 2 && rows[1].detail["suppressed"] == float64(1) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("rows after shutdown = %+v, want the row and a summary with suppressed=1", rows)
		}
		time.Sleep(20 * time.Millisecond)
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
