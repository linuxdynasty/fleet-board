package daemon_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// A stop writes the machine.enroll_failed summary of the minute in progress
// before the store closes: after Run returns, the file holds the refusal's
// row and the summary of the one the cap suppressed.
func TestStoppingTheHubWritesTheRefusalSummary(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	pool := caPool(t, h.storedIdentity(t))
	opts := h.options()
	at := time.Now().Truncate(time.Minute).Add(30 * time.Second) // both refusals in one minute
	opts.Now = func() time.Time { return at }
	r := h.start(t, opts)
	hc := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS13}, ForceAttemptHTTP2: true,
	}}
	defer hc.CloseIdleConnections()
	client := agentv1connect.NewAgentServiceClient(hc, "https://127.0.0.1:"+port(r.addrs.Agent), connect.WithGRPC())
	for range 2 {
		_, err := client.Enroll(context.Background(), connect.NewRequest(&agentv1.EnrollRequest{}))
		if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
			t.Fatalf("Enroll with no install_id: code %v, want invalid_argument", code)
		}
	}
	r.stop(t)
	db, err := sql.Open("sqlite", statedb.Path(h.state))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var rows, summaries int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*), count(occurred_time) FROM audit_log
		WHERE action = 'machine.enroll_failed'`).Scan(&rows, &summaries); err != nil {
		t.Fatal(err)
	}
	if rows != 2 || summaries != 1 {
		t.Errorf("%d machine.enroll_failed rows (%d summaries), want the refusal's row and one summary", rows, summaries)
	}
}
