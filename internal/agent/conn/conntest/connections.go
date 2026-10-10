package conntest

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Connection is one machine_connections row: its epoch and, once closed,
// its end_reason (empty while live).
type Connection struct {
	Epoch     uint64
	EndReason string
}

// current is the state.db of the listener process serving now.
func (h *Hub) current() *store.Store {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.store
}

// Restart stops the listener the way fleetd run does (Shutdown: every live
// Connect stream gets Bye{HUB_RESTART}; then FlushAudit), closes state.db, then
// opens it again and serves a new listener process on the same address: a
// new hub_instance_id and a fresh Hello rate limit, the same store.
func (h *Hub) Restart(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	srv, s := h.srv, h.store
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	srv.FlushAudit(ctx)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	h.serve(t, h.Addr)
}

// LastEpoch is machines.last_epoch of machineID.
func (h *Hub) LastEpoch(t *testing.T, machineID string) uint64 {
	t.Helper()
	var n int64
	h.read(t, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT last_epoch FROM machines WHERE machine_id = ?`, machineID).Scan(&n)
	})
	return uint64(n) //nolint:gosec // G115: last_epoch has CHECK >= 0
}

// Connections are machineID's machine_connections rows, oldest first.
func (h *Hub) Connections(t *testing.T, machineID string) []Connection {
	t.Helper()
	var out []Connection
	h.read(t, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(),
			`SELECT epoch, ifnull(end_reason, '') FROM machine_connections WHERE machine_id = ? ORDER BY epoch`, machineID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c Connection
			var epoch int64
			if err := rows.Scan(&epoch, &c.EndReason); err != nil {
				return err
			}
			c.Epoch = uint64(epoch) //nolint:gosec // G115: epoch has CHECK >= 1
			out = append(out, c)
		}
		return rows.Err()
	})
	return out
}

// HoldWriter blocks state.db's writer until the returned func runs (once;
// the test's end runs it too), so a Hello waits for its job. Restart
// waits for it in Close.
func (h *Hub) HoldWriter(t *testing.T) func() {
	t.Helper()
	release := make(chan struct{})
	held, err := h.current().Submit(context.Background(), func(*sql.Tx) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	free := func() { once.Do(func() { close(release); <-held }) }
	t.Cleanup(free)
	return free
}
