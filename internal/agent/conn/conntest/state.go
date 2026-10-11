package conntest

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// SetState writes machines.state of machineID through the store's writer,
// for a test that needs a state the agent's own Snapshot did not set.
func (h *Hub) SetState(t *testing.T, machineID, state string) {
	t.Helper()
	err := h.current().Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE machines SET state = ? WHERE machine_id = ?`, state, machineID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// State is machines.state of machineID.
func (h *Hub) State(t *testing.T, machineID string) string {
	t.Helper()
	var s string
	h.read(t, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT state FROM machines WHERE machine_id = ?`, machineID).Scan(&s)
	})
	return s
}

// Housekeep runs the hub's machine-state job once at now (fleetd run's
// housekeeping tick): STALE at 45 s of silence, OFFLINE and the stream cut
// at 3 minutes.
func (h *Hub) Housekeep(t *testing.T, now time.Time) {
	t.Helper()
	h.mu.Lock()
	srv := h.srv
	h.mu.Unlock()
	if err := srv.Housekeep(context.Background(), now); err != nil {
		t.Fatalf("Housekeep: %v", err)
	}
}
