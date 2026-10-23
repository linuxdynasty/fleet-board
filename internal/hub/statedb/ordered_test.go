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
// is the Hello-during-shutdown shape: the job must land exactly once. The
// publisher hands rows over a channel: the test never touches hook memory
// without a happens-after edge.
func TestWriteHookedCommitsAfterCallerGone(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	published := make(chan []statedb.AuditStored, 1)
	s.SetAuditPublisher(func(rows []statedb.AuditStored) {
		published <- append([]statedb.AuditStored(nil), rows...)
	})
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
	select {
	case got := <-published:
		if len(got) != 1 || got[0].Action != "test.gone" {
			t.Fatalf("hook published %d audits: want exactly the stamped one", len(got))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hook never published: the cancelled caller's job was lost")
	}
}
