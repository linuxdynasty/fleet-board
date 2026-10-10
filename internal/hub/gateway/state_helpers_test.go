package gateway_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// online connects m on a new stream and seeds its machine ONLINE (plan
// row 5c's Snapshot transaction does that in the product; 5b starts from
// a seeded row).
func (h *hub) online(t *testing.T, m machine) *agentStream {
	t.Helper()
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	h.exec(t, `UPDATE machines SET state = 'ONLINE' WHERE machine_id = ?`, m.id)
	return s
}

// machineState is machines.state and last_seen_time.
func (h *hub) machineState(t *testing.T, machineID string) (string, sql.NullInt64) {
	t.Helper()
	var (
		state string
		seen  sql.NullInt64
	)
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT state, last_seen_time FROM machines WHERE machine_id = ?`,
			machineID).Scan(&state, &seen)
	})
	if err != nil {
		t.Fatal(err)
	}
	return state, seen
}

// waitState polls until machineID is in state want.
func (h *hub) waitState(t *testing.T, machineID, want string) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		got, _ := h.machineState(t, machineID)
		if got == want {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("machine state %s, want %s", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// housekeep moves the hub clock by d and runs the machine-state job once.
func (h *hub) housekeep(t *testing.T, d time.Duration) {
	t.Helper()
	h.clock.set(h.clock.now().Add(d))
	if err := h.srv.Housekeep(context.Background(), h.clock.now()); err != nil {
		t.Fatalf("Housekeep: %v", err)
	}
}

// stateChanges are the machine.state_changed lines for machineID.
func (h *hub) stateChanges(t *testing.T, machineID string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range h.events(t, "machine.state_changed") {
		if l["machine_id"] == machineID {
			out = append(out, l)
		}
	}
	return out
}

// sentHeartbeat is a Heartbeat whose sent_time is at.
func sentHeartbeat(at time.Time) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{
		SentTime: timestamppb.New(at),
	}}}
}

// writerJobs is the number of state.db writer jobs committed so far.
func (h *hub) writerJobs(t *testing.T) float64 {
	t.Helper()
	return metricSum(scrape(t, h.fixture), "fleet_hub_writer_jobs_total")
}

// waitClosed waits for the reconnect.closed line of the stream of epoch.
func (h *hub) waitClosed(t *testing.T, epoch int) map[string]any {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		for _, l := range h.events(t, "reconnect.closed") {
			if l["epoch"] == float64(epoch) {
				return l
			}
		}
		if time.Now().After(end) {
			t.Fatalf("no reconnect.closed for epoch %d", epoch)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitChanges waits until machineID has n machine.state_changed lines: a
// stream's close logs its change after the commit a test can already see.
func (h *hub) waitChanges(t *testing.T, machineID string, n int) []map[string]any {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		got := h.stateChanges(t, machineID)
		if len(got) >= n || time.Now().After(end) {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
}
