package conntest

import (
	"context"
	"database/sql"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// Enqueue puts cmds in the hub's outbox for machineID the way a product
// caller does: outbox.Enqueue inside one writer job of state.db, then,
// once it committed, the listener's Outbox().Notify with the rows.
func (h *Hub) Enqueue(t *testing.T, machineID string, cmds ...*agentv1.Command) []outbox.Op {
	t.Helper()
	h.mu.Lock()
	s, srv := h.store, h.srv
	h.mu.Unlock()
	now := time.Now()
	var ops []outbox.Op
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		ops = ops[:0]
		for _, c := range cmds {
			op, err := outbox.Enqueue(context.Background(), tx, outbox.In{MachineID: machineID, Command: c, Now: now})
			if err != nil {
				return err
			}
			ops = append(ops, op)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	srv.Outbox().Notify(ops...)
	return ops
}

// CommandRow is one commands row.
type CommandRow struct {
	CommandID, State, OpState, Status string
	// SentEpoch and SentSeq are the last recorded send's; 0 when never sent.
	SentEpoch, SentSeq uint64
	// Acked is whether a CommandAck set ack_time.
	Acked bool
}

// Commands are machineID's commands rows in enqueue order.
func (h *Hub) Commands(t *testing.T, machineID string) []CommandRow {
	t.Helper()
	var out []CommandRow
	h.read(t, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT command_id, state, op_state, status,
			ifnull(sent_epoch, 0), ifnull(sent_seq, 0), ack_time IS NOT NULL FROM commands WHERE machine_id = ? ORDER BY id`,
			machineID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var c CommandRow
			var epoch, seq int64
			if err := rows.Scan(&c.CommandID, &c.State, &c.OpState, &c.Status, &epoch, &seq, &c.Acked); err != nil {
				return err
			}
			c.SentEpoch, c.SentSeq = uint64(epoch), uint64(seq) //nolint:gosec // G115: both have CHECK >= 1 when set
			out = append(out, c)
		}
		return rows.Err()
	})
	return out
}

// ResultRow is one command_results row.
type ResultRow struct {
	CommandID, Status, Message string
	// Epoch is the epoch of the stream the result came on.
	Epoch uint64
}

// Results are machineID's command_results rows, oldest first.
func (h *Hub) Results(t *testing.T, machineID string) []ResultRow {
	t.Helper()
	var out []ResultRow
	h.read(t, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT command_id, status, message, epoch FROM command_results
			WHERE machine_id = ? ORDER BY received_time, rowid`, machineID)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r ResultRow
			var epoch int64
			if err := rows.Scan(&r.CommandID, &r.Status, &r.Message, &epoch); err != nil {
				return err
			}
			r.Epoch = uint64(epoch) //nolint:gosec // G115: epoch has CHECK >= 1
			out = append(out, r)
		}
		return rows.Err()
	})
	return out
}

// LogText is every log line the hub wrote so far (JSON, one per line), so
// a test can check the order of two events.
func (h *Hub) LogText() string { return h.logs.String() }
