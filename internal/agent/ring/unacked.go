package ring

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// UnackedCount is how many finished results the hub has not acknowledged:
// the length [DB.Unacked] would return, read from memory. The heartbeat's
// AgentHealth.unacked_results reads it on the connection's sending goroutine,
// which may not wait for the store (conn.LoopOptions.Heartbeat).
//
// It is seeded at [Open] by one query and moved by the writer's own jobs
// (RecordResult +1, AckResults -n, Prune -n), each applied only after the
// batch holding the job committed. A job whose caller gave up waiting still
// moves it when it commits (see [DB.writeThen]).
func (d *DB) UnackedCount() int { return int(max(0, d.unacked.Load())) }

// seedUnacked counts the unacked results in the file once, at Open, before
// any other job can run.
func (d *DB) seedUnacked(ctx context.Context) error {
	var n int64
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = countUnacked(ctx, tx)
		return err
	})
	if err != nil {
		return err
	}
	d.unacked.Store(n)
	return nil
}

// countUnacked is the number of finished entries with no ack time, the
// condition [DB.Unacked] reads.
func countUnacked(ctx context.Context, tx *sql.Tx) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM command_log WHERE status = ? AND acked_time IS NULL`,
		string(StatusFinished)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("ring: count unacked results: %w", err)
	}
	return n, nil
}

// writeThen is Write that runs after once the job's batch has committed.
// When ctx ends first the outcome is unknown (store.Write: the job may still
// commit), so after runs from a goroutine that waits for the writer's answer;
// the answer is always sent (the channel is buffered and the writer drains its
// queue on Close). after reads what the job left in the caller's variables;
// the writer's send orders those writes before it.
func (d *DB) writeThen(ctx context.Context, apply store.ApplyFunc, after func()) error {
	done, err := d.s.Submit(ctx, apply)
	if err != nil {
		return err
	}
	select {
	case err := <-done:
		if err == nil {
			after()
		}
		return err
	case <-ctx.Done():
		go func() {
			if <-done == nil {
				after()
			}
		}()
		return ctx.Err()
	}
}
