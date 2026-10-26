package statedb_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestMachineHarnessFailsFirst proves the harness inventory of row 22
// before it exists: snapshots replace the machine's harness rows (the
// catalog hash and goal-control account the agent reported), and dispatch
// reads back what the snapshot said, never a hub-kept table.
func TestMachineHarnessFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	replace := func(rows []statedb.HarnessInventory) {
		t.Helper()
		err := s.Write(ctx, func(tx *sql.Tx) error {
			return statedb.ReplaceMachineHarness(ctx, tx, "m1", rows, 1000)
		})
		if err != nil {
			t.Fatalf("replace: %v", err)
		}
	}
	read := func(harness string) (statedb.HarnessInventory, bool) {
		t.Helper()
		var inv statedb.HarnessInventory
		var found bool
		err := s.Read(ctx, func(tx *sql.Tx) error {
			var err error
			inv, found, err = statedb.ReadMachineHarnessTx(ctx, tx, "m1", harness)
			return err
		})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return inv, found
	}
	replace([]statedb.HarnessInventory{
		{MachineID: "m1", Harness: "claude", Version: "2.1.286", CatalogHash: "abc",
			GoalControlJSON: `{"pauseMode":"GOAL_PAUSE_MODE_INTERRUPT"}`, LaunchAllowed: true},
		{MachineID: "m1", Harness: "codex", Version: "1.0", CatalogHash: "def",
			GoalControlJSON: `{}`, LaunchAllowed: true},
	})
	got, found := read("claude")
	if !found || got.CatalogHash != "abc" || !got.LaunchAllowed {
		t.Fatalf("claude = %+v, found %v, want the snapshot's row", got, found)
	}
	replace([]statedb.HarnessInventory{
		{MachineID: "m1", Harness: "claude", Version: "2.1.287", CatalogHash: "abc2",
			GoalControlJSON: `{}`, LaunchAllowed: true},
	})
	if _, found := read("codex"); found {
		t.Fatalf("codex survived a replace without it")
	}
	got, found = read("claude")
	if !found || got.Version != "2.1.287" {
		t.Fatalf("claude = %+v, found %v, want the new snapshot", got, found)
	}
}
