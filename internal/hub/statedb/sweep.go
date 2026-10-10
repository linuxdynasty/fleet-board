package statedb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Swept is what [StartSweep] committed.
type Swept struct {
	// Changes are the machines that were ONLINE and are now STALE.
	Changes []StateChange
	// RowsClosed counts the live machine_connections rows closed as
	// hub_restart.
	RowsClosed int
}

// StartSweep is the hub-start pass (03n "Hub start"), one writer job run
// before the agent listener serves, so no Hello can race it. Every live
// machine_connections row is closed as hub_restart: no stream of this
// process holds it (a crash, or a shutdown that stopped waiting for a
// Hello whose job then committed), and it would otherwise stay live until
// the machine's next Hello. Every ONLINE machine becomes STALE (reason
// hub_start, stamped), so the 3-minute rule, counted from now, turns the
// ones that do not come back OFFLINE. No other state changes.
func StartSweep(ctx context.Context, s *store.Store, now time.Time) (Swept, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out Swept
	err := s.Write(ctx, func(tx *sql.Tx) error {
		out = Swept{}
		res, err := tx.ExecContext(jctx, `UPDATE machine_connections SET ended_time = max(?, started_time), end_reason = ?
			WHERE ended_time IS NULL`, now.UnixMilli(), string(EndHubRestart))
		if err != nil {
			return fmt.Errorf("statedb: close live connections: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("statedb: close live connections: %w", err)
		}
		out.RowsClosed = int(n)
		ids, err := machinesIn(jctx, tx, MachineOnline)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := changeState(jctx, tx, id, []string{MachineOnline}, MachineStale, ReasonHubStart, now)
			if err != nil {
				return err
			}
			out.Changes = append(out.Changes, *c)
		}
		return nil
	})
	if err != nil {
		return Swept{}, err
	}
	return out, nil
}

func machinesIn(ctx context.Context, tx *sql.Tx, state string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT machine_id FROM machines WHERE state = ? ORDER BY machine_id`, state)
	if err != nil {
		return nil, fmt.Errorf("statedb: list %s machines: %w", state, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("statedb: list %s machines: %w", state, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("statedb: list %s machines: %w", state, err)
	}
	return ids, nil
}

// MachineLive is one machine as housekeeping reads it: its stored state,
// its live connection (Epoch 0 and a zero Started when none) and its
// last_seen_time (zero when never seen). A [Mark] carries LastSeen back as
// Seen, so the job can tell that something was heard after the read.
type MachineLive struct {
	MachineID string
	State     string
	Epoch     uint64
	Started   time.Time
	LastSeen  time.Time
}

// MachineStates lists every machine with its live connection, on the read
// pool (03f: housekeeping reads a consistent snapshot, then writes jobs).
func MachineStates(ctx context.Context, s *store.Store) ([]MachineLive, error) {
	var out []MachineLive
	err := s.Read(ctx, func(tx *sql.Tx) error {
		out = nil
		rows, err := tx.QueryContext(ctx, `SELECT m.machine_id, m.state, ifnull(c.epoch, 0), ifnull(c.started_time, 0),
			ifnull(m.last_seen_time, 0) FROM machines m LEFT JOIN machine_connections c ON c.machine_id = m.machine_id AND c.ended_time IS NULL
			ORDER BY m.machine_id`)
		if err != nil {
			return fmt.Errorf("statedb: list machine states: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				m                    MachineLive
				epoch, started, seen int64
			)
			if err := rows.Scan(&m.MachineID, &m.State, &epoch, &started, &seen); err != nil {
				return fmt.Errorf("statedb: list machine states: %w", err)
			}
			m.Epoch = uint64(epoch) //nolint:gosec // G115: epoch has CHECK BETWEEN 1 AND MaxEpoch
			if epoch != 0 {
				m.Started = time.UnixMilli(started)
			}
			if seen != 0 {
				m.LastSeen = time.UnixMilli(seen)
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
