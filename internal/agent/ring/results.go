package ring

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// The command log keeps the last MaxCommands entries or MaxCommandAge
// (defaults.md "Agent command log").
const (
	MaxCommands   = 10_000
	MaxCommandAge = 7 * 24 * time.Hour
)

// Unacked returns the finished entries the hub has not acknowledged, oldest
// result first. They are re-sent after every Snapshot until ResultAck (02i).
func (d *DB) Unacked(ctx context.Context) ([]Entry, error) {
	var out []Entry
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		list, err := unackedIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range list {
			e, err := readEntry(ctx, tx, id)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return nil
	})
	return out, err
}

func unackedIDs(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command_id FROM command_log
		WHERE status = ? AND acked_time IS NULL ORDER BY finished_time, received_time, command_id`,
		string(StatusFinished))
	if err != nil {
		return nil, fmt.Errorf("ring: read unacked results: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var list []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ring: read unacked results: %w", err)
		}
		list = append(list, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ring: read unacked results: %w", err)
	}
	return list, nil
}

// AckResults stamps the ResultAck time on each finished, not yet acked entry
// among ids and returns how many it stamped. An unknown id (never logged, or
// pruned), one not finished yet and one already acked change nothing.
func (d *DB) AckResults(ctx context.Context, ids []string, at time.Time) (int, error) {
	var n int64
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		st, err := tx.PrepareContext(ctx, `UPDATE command_log SET acked_time = ?
			WHERE command_id = ? AND status = ? AND acked_time IS NULL`)
		if err != nil {
			return fmt.Errorf("ring: ack results: %w", err)
		}
		defer func() { _ = st.Close() }()
		for _, id := range ids {
			res, err := st.ExecContext(ctx, at.UnixMilli(), id, string(StatusFinished))
			if err != nil {
				return fmt.Errorf("ring: ack results: %w", err)
			}
			k, err := res.RowsAffected()
			if err != nil {
				return fmt.Errorf("ring: ack results: %w", err)
			}
			n += k
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// forgettable is the SQL condition for an entry the count cap may delete:
// acked, or past its deadline, so the hub no longer re-sends it.
const forgettable = `(acked_time IS NOT NULL OR not_after_time < ?)`

// Prune deletes the entries received more than [MaxCommandAge] before now,
// then, while more than [MaxCommands] remain, the oldest that are acked or
// past their not_after_time. A command still inside its deadline and not
// acked is never deleted for the count: the hub may re-send it, and a
// forgotten command_id would run twice. Prune returns how many it deleted.
// A pruned command_id is unknown afterwards: a ResultAck for it changes
// nothing.
func (d *DB) Prune(ctx context.Context, now time.Time) (int, error) {
	old, at := now.Add(-MaxCommandAge).UnixMilli(), now.UnixMilli()
	// A read first spares the write, and its batch wait, when nothing is due.
	var due bool
	if err := d.s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM command_log WHERE received_time < ?)
			OR ((SELECT count(*) FROM command_log) > ? AND EXISTS(SELECT 1 FROM command_log WHERE `+forgettable+`))`,
			old, MaxCommands, at).Scan(&due)
	}); err != nil {
		return 0, fmt.Errorf("ring: prune command log: %w", err)
	}
	if !due {
		return 0, nil
	}
	var n int64
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM command_log WHERE received_time < ?`, old)
		if err != nil {
			return fmt.Errorf("ring: prune command log: %w", err)
		}
		if n, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("ring: prune command log: %w", err)
		}
		res, err = tx.ExecContext(ctx, `DELETE FROM command_log WHERE command_id IN (
			SELECT command_id FROM command_log WHERE `+forgettable+`
			ORDER BY received_time, command_id
			LIMIT max(0, (SELECT count(*) FROM command_log) - ?))`, at, MaxCommands)
		if err != nil {
			return fmt.Errorf("ring: prune command log: %w", err)
		}
		k, err := res.RowsAffected()
		n += k
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
