package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestListAndReadGoalRows covers the row-12 goal read path: every stored
// goal lists, one reads back by id, and an unknown id misses with
// ErrGoalUnknown.
func TestListAndReadGoalRows(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)

	writeObserved(t, s, observedRow("obs-1"))
	row := observedRow("obs-2")
	row.MachineID = "m2"
	writeObserved(t, s, row)

	rows, err := statedb.ListGoalRows(context.Background(), s)
	if err != nil {
		t.Fatalf("ListGoalRows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 observed goals", len(rows))
	}
	got, err := statedb.ReadGoalRow(context.Background(), s, "obs-2")
	if err != nil {
		t.Fatalf("ReadGoalRow: %v", err)
	}
	if got.MachineID != "m2" || got.Spec == "" || got.Status == "" {
		t.Errorf("row = %+v, want the m2 spec and status", got)
	}
	if _, err := statedb.ReadGoalRow(context.Background(), s, "obs-missing"); !errors.Is(err, statedb.ErrGoalUnknown) {
		t.Errorf("missing id: got %v, want ErrGoalUnknown", err)
	}
}
