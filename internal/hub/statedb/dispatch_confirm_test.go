package statedb_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestDispatchConfirmReasonsRoundTrip proves migration 32: the CONFIRM
// and WARN reason sets an approval was raised on persist with the stored
// input, and an empty set reads back empty.
func TestDispatchConfirmReasonsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()

	withReasons := statedb.DispatchConfirm{
		ApprovalID: "appr-1", InputJSON: `{"requestId":"req-1"}`, MachineID: "m1",
		ConfirmReasons: []string{"REASON_CODE_WORK_ITEM_UNTRUSTED"},
		WarnReasons:    []string{"REASON_CODE_ACCOUNT_USAGE_UNKNOWN"},
		CreateMs:       1000,
	}
	withoutReasons := statedb.DispatchConfirm{
		ApprovalID: "appr-2", InputJSON: `{"requestId":"req-2"}`, MachineID: "m1",
		CreateMs: 2000,
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		if err := statedb.InsertDispatchConfirm(ctx, tx, withReasons); err != nil {
			return err
		}
		return statedb.InsertDispatchConfirm(ctx, tx, withoutReasons)
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		got, err := statedb.ReadDispatchConfirmTx(ctx, tx, "appr-1")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got.ConfirmReasons, withReasons.ConfirmReasons) {
			t.Fatalf("confirms = %v, want %v", got.ConfirmReasons, withReasons.ConfirmReasons)
		}
		if !reflect.DeepEqual(got.WarnReasons, withReasons.WarnReasons) {
			t.Fatalf("warns = %v, want %v", got.WarnReasons, withReasons.WarnReasons)
		}
		empty, err := statedb.ReadDispatchConfirmTx(ctx, tx, "appr-2")
		if err != nil {
			return err
		}
		if len(empty.ConfirmReasons) != 0 || len(empty.WarnReasons) != 0 {
			t.Fatalf("empty reasons = %v/%v, want empty", empty.ConfirmReasons, empty.WarnReasons)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}
