package statedb_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The account list helper reads back every owner-created account, so the
// hub API can filter, sort and page accounts in memory.
func TestAccountListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	seed := func(id, harness, displayName, requestID string) {
		t.Helper()
		spec := `{"displayName":"` + displayName + `","mode":"ACCOUNT_MODE_FLEET",` +
			`"budgetKind":"BUDGET_KIND_DOLLAR_CAP"}`
		if _, err := statedb.CreateOwnerAccount(ctx, s, statedb.CreateAccount{
			ConfigWrite: statedb.ConfigWrite{
				Principal: "local:alice", RequestID: requestID, Now: now,
			},
			ID: id, Harness: harness, Provider: "anthropic",
			DisplayName: displayName, Origin: statedb.AccountOriginOwner,
			Spec: []byte(spec),
		}, nil); err != nil {
			t.Fatalf("CreateOwnerAccount %s: %v", id, err)
		}
	}
	seed("01kacclist000000000000000001", "claude", "work", "req-1")
	seed("01kacclist000000000000000002", "opencode", "side", "req-2")

	rows, err := statedb.ListStoredAccounts(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 accounts", len(rows))
	}

	got, err := statedb.GetAccount(ctx, s, "01kacclist000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Harness != "claude" || got.Provider != "anthropic" {
		t.Errorf("row %+v, want version 1 reached through anthropic", got)
	}
	if got.CreateMs != now.UnixMilli() || got.UpdateMs != now.UnixMilli() {
		t.Errorf("create %d update %d, want the write time for both", got.CreateMs, got.UpdateMs)
	}

	if _, err := statedb.GetAccount(ctx, s, "01kacclist000000000000000099"); !errors.Is(err, statedb.ErrAccountUnknown) {
		t.Errorf("unknown account: %v, want ErrAccountUnknown", err)
	}
}
