package statedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// A writer job can commit after its caller is gone: the in-tx capture
// reads use a context that survives cancellation, so a cancelled waiter
// loses only its own outcome, never the commit or its audits (#403). This
// is the Hello-during-shutdown shape: the job must land exactly once.
func TestWriteHookedCommitsAfterCallerGone(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	var got []statedb.AuditStored
	s.SetAuditPublisher(func(rows []statedb.AuditStored) { got = append(got, rows...) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller is gone before the job runs
	now := time.Now().UTC()
	_ = statedb.WriteHooked(ctx, s, func(tx *sql.Tx) error {
		_, err := statedb.AppendAudit(context.Background(), tx, statedb.AuditEntry{
			Actor:  statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Action: "test.gone", Target: "test/gone", Now: now,
		})
		return err
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		max, err := statedb.MaxAuditSeq(context.Background(), s)
		if err != nil {
			t.Fatalf("MaxAuditSeq: %v", err)
		}
		if max > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 || got[0].Action != "test.gone" {
		t.Fatalf("hook published %d audits: the cancelled caller's job never published", len(got))
	}
}
