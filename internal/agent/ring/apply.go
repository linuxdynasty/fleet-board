package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LogAndApply logs c and records its seq as the last applied command of its
// epoch, in one store job (ruling fork 4 i; 02i "What agent.db keeps"), so a
// crash never leaves a seq applied with no log row. A known c.ID is checked
// first: nothing is saved and the logged entry comes back with dup set,
// whatever c's epoch and seq. Otherwise an epoch that is not the current one
// is [ErrWrongEpoch] and a seq at or below the last applied one
// [ErrSeqReplayed]; neither saves anything.
func (d *DB) LogAndApply(ctx context.Context, c Command) (Entry, bool, error) {
	epoch, seq, err := c.ints()
	if err != nil {
		return Entry{}, false, err
	}
	var out Entry
	var dup bool
	err = d.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := readEntry(ctx, tx, c.ID)
		if err == nil {
			out, dup = e, true
			return nil
		}
		if !errors.Is(err, ErrUnknownCommand) {
			return err
		}
		st, err := readState(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case st.Epoch == 0 || c.Epoch != st.Epoch:
			return fmt.Errorf("%w: %d, current %d", ErrWrongEpoch, c.Epoch, st.Epoch)
		case c.Seq <= st.CommandSeq:
			return fmt.Errorf("%w: %d after %d", ErrSeqReplayed, c.Seq, st.CommandSeq)
		}
		if out, _, err = insertCommand(ctx, tx, c, epoch, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_state SET last_applied_command_seq = ? WHERE id = 1`, seq)
		if err != nil {
			return fmt.Errorf("ring: apply command seq: %w", err)
		}
		return nil
	})
	if err != nil {
		return Entry{}, false, err
	}
	return out, dup, nil
}

// SetRunning marks a logged entry running and raises its step reached to
// step (a lower step keeps the higher one). A finished entry is
// [ErrResultLogged]; an unknown id [ErrUnknownCommand].
func (d *DB) SetRunning(ctx context.Context, id string, step int) error {
	if step < 0 {
		return fmt.Errorf("ring: step %d is negative", step)
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		e, err := readEntry(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.Status == StatusFinished {
			return ErrResultLogged
		}
		_, err = tx.ExecContext(ctx, `UPDATE command_log SET status = ?, step_reached = max(step_reached, ?)
			WHERE command_id = ?`, string(StatusRunning), step, id)
		if err != nil {
			return fmt.Errorf("ring: mark running: %w", err)
		}
		return nil
	})
}

// Open returns every entry with no result yet (received or running), in
// the order they were applied: epoch, seq, then received time. The executor
// resumes them after a restart (ruling fork 4 iii).
func (d *DB) Open(ctx context.Context) ([]Entry, error) {
	var out []Entry
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT command_id FROM command_log WHERE status <> ?
			ORDER BY epoch, seq, received_time, command_id`, string(StatusFinished))
		if err != nil {
			return fmt.Errorf("ring: read open commands: %w", err)
		}
		defer func() { _ = rows.Close() }()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("ring: read open commands: %w", err)
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("ring: read open commands: %w", err)
		}
		for _, id := range ids {
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
