package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// A SnapshotRequest{seq_rollback} that finds the stream's send queue full
// is not lost silently: it is counted and logged.
func TestADroppedSnapshotRequestIsCounted(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	a, tel := newTestDepsTo(t, &logs)
	s := newStream(context.Background(), "m1", "boot-a", "p1", 3, time.Now())
	for s.send(&agentv1.ConnectResponse{}) {
	}
	a.rolledBack(context.Background(), s, statedb.SnapshotOut{Generation: 2, Rollback: []string{"snapshot_seq"}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	line := grepLines(rec.Body.String(), "fleet_hub_snapshot_requests_dropped_total{")
	if !strings.Contains(line, `machine="m1"`) || !strings.HasSuffix(line, " 1") {
		t.Errorf("fleet_hub_snapshot_requests_dropped_total = %q, want 1 for machine m1", line)
	}
	if !strings.Contains(logs.String(), `"msg":"snapshot.request_dropped"`) {
		t.Errorf("no snapshot.request_dropped line in %s", logs.String())
	}
}

func grepLines(s, sub string) string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
