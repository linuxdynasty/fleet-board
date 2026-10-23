package statedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The reserve in force when a window ended sets its usable line (05
// section 3): creating an account records its reserve from its creation
// stamp, and a spec update that changes the reserve appends a row
// stamped at the update. Neither the audit detail ({id, version}) nor
// the 24-hour config_request_log keeps past specs, so this history is
// the only durable source of past reserves.
func TestAccountReserveHistoryRecordsCreateAndUpdate(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	ctx := context.Background()
	created := accountTime
	updated := accountTime.Add(time.Hour)

	createWithReserve := func(t *testing.T, at time.Time, reserve string) {
		t.Helper()
		w := configWrite("local:alice", "req-res-1", 'r')
		w.Now = at
		if _, err := statedb.CreateOwnerAccount(ctx, s, statedb.CreateAccount{
			ConfigWrite: w, ID: "acc-res-1", Harness: "opencode", Provider: "anthropic",
			DisplayName: "work", Origin: statedb.AccountOriginOwner,
			Spec: []byte(`{"displayName":"work","reservePercent":` + reserve + `}`),
		}, nil); err != nil {
			t.Fatalf("CreateOwnerAccount: %v", err)
		}
	}
	updateToReserve := func(t *testing.T, reqID string, at time.Time, spec string) {
		t.Helper()
		merge := func(current []byte) ([]byte, string, error) {
			return []byte(spec), "work", nil
		}
		w := configWrite("local:alice", reqID, reqID[0])
		w.Now = at
		if _, err := statedb.UpdateOwnerAccount(ctx, s, statedb.UpdateAccountOp{
			ConfigWrite: w, ID: "acc-res-1", ExpectedVersion: 0, Merge: merge,
		}, nil); err != nil {
			t.Fatalf("UpdateOwnerAccount: %v", err)
		}
	}

	createWithReserve(t, created, "20")
	got, err := statedb.ListAccountReserveHistory(ctx, s, "acc-res-1")
	if err != nil {
		t.Fatalf("ListAccountReserveHistory: %v", err)
	}
	if len(got) != 1 || !got[0].From.Equal(created) || got[0].Percent != 20 {
		t.Fatalf("history after create = %+v, want [(%v, 20)]", got, created)
	}

	updateToReserve(t, "req-res-2", updated, `{"displayName":"work","reservePercent":30}`)
	got, err = statedb.ListAccountReserveHistory(ctx, s, "acc-res-1")
	if err != nil {
		t.Fatalf("ListAccountReserveHistory: %v", err)
	}
	if len(got) != 2 || !got[1].From.Equal(updated) || got[1].Percent != 30 {
		t.Fatalf("history after update = %+v, want [(%v, 20) (%v, 30)]", got, created, updated)
	}

	// An update that keeps the reserve appends no row.
	updateToReserve(t, "req-res-3", updated.Add(time.Hour), `{"displayName":"renamed","reservePercent":30}`)
	got, err = statedb.ListAccountReserveHistory(ctx, s, "acc-res-1")
	if err != nil {
		t.Fatalf("ListAccountReserveHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history after a reserve-keeping update = %+v, want still 2 rows", got)
	}
}

// An account created before the history table existed has no rows: the
// rollup falls back to the current reserve, as before.
func TestAccountReserveHistoryEmptyForUnknownAccount(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	got, err := statedb.ListAccountReserveHistory(context.Background(), s, "acc-missing")
	if err != nil {
		t.Fatalf("ListAccountReserveHistory: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("history = %+v, want no rows for an unknown account", got)
	}
}
