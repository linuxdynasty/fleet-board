package statedb_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestMachineUnfitFailsFirst proves the durable unfit markers of 03h
// before they exist: a start failure marks its scope, a pinned dispatch
// sees it, and a fresh fact (or the hub's re-check) clears it.
func TestMachineUnfitFailsFirst(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	mark := statedb.UnfitMark{MachineID: "m1", Harness: "claude", Repo: "acme/web",
		Reason: "REASON_CODE_PUSH_ACCESS_MISSING", SinceMs: 1000, NextCheckMs: 2000}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return statedb.SetUnfit(ctx, tx, mark)
	}); err != nil {
		t.Fatalf("mark: %v", err)
	}
	var marks []statedb.UnfitMark
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		marks, err = statedb.ListUnfit(ctx, tx, "m1")
		return err
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(marks) != 1 || marks[0].Reason != "REASON_CODE_PUSH_ACCESS_MISSING" {
		t.Fatalf("marks = %+v, want the push-access mark", marks)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return statedb.ClearUnfit(ctx, tx, "m1", "claude", "acme/web")
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		marks, err = statedb.ListUnfit(ctx, tx, "m1")
		return err
	}); err != nil {
		t.Fatalf("relist: %v", err)
	}
	if len(marks) != 0 {
		t.Fatalf("marks = %+v, want cleared", marks)
	}
}
