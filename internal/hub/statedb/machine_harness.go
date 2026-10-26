package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// machineHarnessSQL is migration 31 (plan row 22): the harness inventory
// the hub needs for goal control. The agent reports its catalog hash and
// goal-control account per harness in every Snapshot; the hub stores what
// the snapshot said and never keeps its own table (02h "Goal control
// support", 02m "Control"). The rows are replaced wholesale per snapshot:
// a harness the snapshot no longer lists is gone from the machine.
const machineHarnessSQL = `CREATE TABLE machine_harness(
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	harness TEXT NOT NULL CHECK (harness <> ''),
	profile TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '',
	catalog_hash TEXT NOT NULL DEFAULT '',
	goal_control_json TEXT NOT NULL DEFAULT '{}',
	launch_allowed INTEGER NOT NULL DEFAULT 0 CHECK (launch_allowed IN (0, 1)),
	update_ms INTEGER NOT NULL,
	PRIMARY KEY (machine_id, harness, profile)
) STRICT;`

// HarnessInventory is one reported harness on one machine: the catalog
// hash RunHarnessCommand must match, the goal-control account as
// protojson, the version, and whether local policy lets the hub launch
// it. Plain types only, so the store stays free of generated types.
type HarnessInventory struct {
	MachineID       string
	Harness         string
	Profile         string
	Version         string
	CatalogHash     string
	GoalControlJSON string
	LaunchAllowed   bool
	UpdateMs        int64
}

// ReplaceMachineHarness stores one snapshot's harness list: every row of
// the machine is replaced, so a harness the snapshot no longer lists
// stops being dispatchable at once.
func ReplaceMachineHarness(ctx context.Context, tx *sql.Tx, machineID string, rows []HarnessInventory, nowMs int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM machine_harness WHERE machine_id = ?`, machineID); err != nil {
		return fmt.Errorf("statedb: clear machine harness: %w", err)
	}
	for _, r := range rows {
		allowed := 0
		if r.LaunchAllowed {
			allowed = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO machine_harness(machine_id, harness, profile, version,
			catalog_hash, goal_control_json, launch_allowed, update_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, machineID, r.Harness, r.Profile, r.Version,
			r.CatalogHash, r.GoalControlJSON, allowed, nowMs); err != nil {
			return fmt.Errorf("statedb: insert machine harness: %w", err)
		}
	}
	return nil
}

// ReadMachineHarnessTx reads one reported harness in the caller's
// transaction, found false when the machine never reported it.
func ReadMachineHarnessTx(ctx context.Context, tx *sql.Tx, machineID, harness string) (HarnessInventory, bool, error) {
	var inv HarnessInventory
	var allowed int64
	err := tx.QueryRowContext(ctx, `SELECT machine_id, harness, profile, version,
		catalog_hash, goal_control_json, launch_allowed, update_ms FROM machine_harness
		WHERE machine_id = ? AND harness = ? ORDER BY profile LIMIT 1`,
		machineID, harness).Scan(&inv.MachineID, &inv.Harness, &inv.Profile, &inv.Version,
		&inv.CatalogHash, &inv.GoalControlJSON, &allowed, &inv.UpdateMs)
	if errors.Is(err, sql.ErrNoRows) {
		return HarnessInventory{}, false, nil
	}
	if err != nil {
		return HarnessInventory{}, false, fmt.Errorf("statedb: read machine harness: %w", err)
	}
	inv.LaunchAllowed = allowed != 0
	return inv, true, nil
}
