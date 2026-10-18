package statedb_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func TestPrecheckRevokeForWriteCanceledBeforeReservation(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	_, s := enrolledMachineStore(t, id, beforeTTL)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	ctx = statedb.WithValidatedWrite(ctx, func() error {
		calls.Add(1)
		return nil
	})
	cancel()
	_, err := statedb.PrecheckRevokeForWrite(ctx, s,
		machineWrite("local:alice", "canceled", 1, beforeTTL.Add(time.Minute)), id, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled precheck = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("admission callback ran %d times after cancellation", got)
	}
}
