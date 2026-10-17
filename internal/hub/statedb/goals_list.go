package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ErrGoalUnknown is a goal read with no row for the id.
var ErrGoalUnknown = errors.New("statedb: no goal row for the id")

// GoalRow is one goals row as the hub stored it: the immutable spec and the
// latest status as protojson, the phase and reason as enum names, and the
// times in UnixMillis. EndMillis is nil while the goal is open. The Goals
// view (hub API reads, row 12) filters, sorts and pages on these columns;
// the proto assembly lives with the API, so this package stays free of
// generated types.
type GoalRow struct {
	ID         string
	MachineID  string
	Origin     string
	Spec       string
	Status     string
	Phase      string
	Reason     string
	StartMs    int64
	EndMs      *int64
	ObservedMs int64
	HubSeq     int64
}

// goalColumns lists every goals column a GoalRow scan reads.
const goalColumns = `id, origin, machine_id, spec, status, phase, phase_reason,
	start_time, end_time, observed_time, hub_seq`

// scanGoalRow reads one goals row, single or from a result set.
func scanGoalRow(row *sql.Row) (GoalRow, error) {
	var g GoalRow
	var end sql.NullInt64
	err := row.Scan(&g.ID, &g.Origin, &g.MachineID, &g.Spec, &g.Status, &g.Phase, &g.Reason,
		&g.StartMs, &end, &g.ObservedMs, &g.HubSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalRow{}, ErrGoalUnknown
	}
	if err != nil {
		return GoalRow{}, fmt.Errorf("statedb: read goal row: %w", err)
	}
	if end.Valid {
		g.EndMs = &end.Int64
	}
	return g, nil
}

// scanGoalRows reads one goals row from a result set.
func scanGoalRows(row *sql.Rows) (GoalRow, error) {
	var g GoalRow
	var end sql.NullInt64
	err := row.Scan(&g.ID, &g.Origin, &g.MachineID, &g.Spec, &g.Status, &g.Phase, &g.Reason,
		&g.StartMs, &end, &g.ObservedMs, &g.HubSeq)
	if err != nil {
		return GoalRow{}, fmt.Errorf("statedb: read goal row: %w", err)
	}
	if end.Valid {
		g.EndMs = &end.Int64
	}
	return g, nil
}

// ReadGoalRow reads one goal row on the read pool
// ([ErrGoalUnknown] when there is no row).
func ReadGoalRow(ctx context.Context, s *store.Store, id string) (GoalRow, error) {
	var out GoalRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = scanGoalRow(tx.QueryRowContext(ctx,
			`SELECT `+goalColumns+` FROM goals WHERE id = ?`, id))
		return err
	})
	return out, err
}

// ListGoalRows reads every goal row on the read pool. Fleets hold hundreds
// of goals, so the API filters, sorts and pages in memory over this
// snapshot instead of building per-field SQL.
func ListGoalRows(ctx context.Context, s *store.Store) ([]GoalRow, error) {
	var out []GoalRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+goalColumns+` FROM goals`)
		if err != nil {
			return fmt.Errorf("statedb: list goal rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			g, err := scanGoalRows(rows)
			if err != nil {
				return err
			}
			out = append(out, g)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
