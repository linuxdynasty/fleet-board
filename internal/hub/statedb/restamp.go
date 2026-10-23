package statedb

import (
	"context"
	"database/sql"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// RestampMachine republishes one machine row at a fresh Watch position:
// the row keeps its state and content, only observed_time and hub_seq move
// to now and the next stamp. The Watch volatile flush calls it when a held
// change comes due, so the delayed message carries a position newer than
// every entry published while it was held; a resume from one of those
// never skips it. A job that changes nothing else still takes a stamp, so
// gaps stay normal (02c). [ErrMachineUnknown] when the row is gone, in
// which case the stamp rolls back with the job and nothing is consumed.
// onCommit runs on the store writer after this job's batch commits, in
// commit order (#393): the Watch publish for exactly this restamp, with
// the row re-read on the writer, where no newer commit exists yet so the
// read sees exactly this job's stamp. A nil onCommit writes without one.
// It must follow the post-commit hook rules (short, non-blocking, never
// Submit or Write).
func RestampMachine(ctx context.Context, s *store.Store, id string, now time.Time, onCommit func(MachineRow)) (MachineRow, error) {
	jctx := context.WithoutCancel(ctx)
	var err error
	var captured MachineRow
	if onCommit == nil {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			return restampMachine(jctx, tx, id, now)
		})
	} else {
		err = s.WriteHook(ctx, func(tx *sql.Tx) error {
			if err := restampMachine(jctx, tx, id, now); err != nil {
				return err
			}
			// Capture the after-image in-tx (#403): the hook must not
			// re-read on the writer.
			var rerr error
			if captured, rerr = readMachineRowTx(jctx, tx, id); rerr != nil {
				return rerr
			}
			return nil
		}, func() {
			onCommit(captured)
		})
	}
	if err != nil {
		return MachineRow{}, err
	}
	return ReadMachineRow(ctx, s, id)
}

func restampMachine(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	st, err := NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE machines SET observed_time = ?, hub_seq = ? WHERE machine_id = ?`,
		st.ObservedTime.UnixMilli(), st.HubSeq, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrMachineUnknown
	}
	return nil
}

// RestampGoal republishes one goal row at a fresh Watch position, as
// [RestampMachine]: only observed_time and hub_seq move. The stored status
// JSON keeps its own copies, but every view rebuilds them from the row
// (goalViewOf), so reads stay consistent. [ErrGoalUnknown] when the row is
// gone, with the stamp rolled back.
func RestampGoal(ctx context.Context, s *store.Store, id string, now time.Time, onCommit func(GoalRow)) (GoalRow, error) {
	jctx := context.WithoutCancel(ctx)
	var err error
	var captured GoalRow
	if onCommit == nil {
		err = s.Write(ctx, func(tx *sql.Tx) error {
			return restampGoal(jctx, tx, id, now)
		})
	} else {
		err = s.WriteHook(ctx, func(tx *sql.Tx) error {
			if err := restampGoal(jctx, tx, id, now); err != nil {
				return err
			}
			// Capture the after-image in-tx (#403).
			var rerr error
			if captured, rerr = ReadGoalTx(jctx, tx, id); rerr != nil {
				return rerr
			}
			return nil
		}, func() {
			onCommit(captured)
		})
	}
	if err != nil {
		return GoalRow{}, err
	}
	return ReadGoalRow(ctx, s, id)
}

func restampGoal(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	st, err := NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE goals SET observed_time = ?, hub_seq = ? WHERE id = ?`,
		st.ObservedTime.UnixMilli(), st.HubSeq, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrGoalUnknown
	}
	return nil
}
