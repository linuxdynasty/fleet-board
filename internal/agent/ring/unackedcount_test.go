package ring_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// sameAsUnacked fails unless UnackedCount (a number the heartbeat reads from
// memory) equals the length of the query it stands in for.
func sameAsUnacked(t *testing.T, db *ring.DB, when string) {
	t.Helper()
	un, err := db.Unacked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := db.UnackedCount(); got != len(un) {
		t.Errorf("%s: UnackedCount = %d, Unacked() holds %d", when, got, len(un))
	}
}

// The heartbeat's unacked_results must not run a query on the sending
// goroutine (conn.LoopOptions.Heartbeat), so the DB keeps the count: seeded
// at Open, then moved by the writer's own jobs. Each step here must leave it
// equal to the length of Unacked().
func TestUnackedCountFollowsRecordAckAndPrune(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := stateDir(t)
	db, err := ring.Open(ctx, dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sameAsUnacked(t, db, "empty file")

	now := t0.Add(30 * 24 * time.Hour)
	for i := range 4 { // c-0..c-3 finished; c-0 and c-1 are past their deadline at now
		c := command(fmt.Sprintf("c-%d", i), 1, uint64(i+1))
		if i >= 2 {
			c.NotAfter = now.Add(time.Hour)
		}
		if _, _, err := db.LogCommand(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := db.RecordResult(ctx, c.ID, []byte("r"), t0.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		sameAsUnacked(t, db, "after RecordResult "+c.ID)
	}
	if got := db.UnackedCount(); got != 4 {
		t.Fatalf("UnackedCount = %d after four results, want 4", got)
	}
	if _, _, err := db.LogCommand(ctx, command("c-running", 1, 9)); err != nil {
		t.Fatal(err)
	}
	sameAsUnacked(t, db, "after a command that has no result")

	if err := db.RecordResult(ctx, "c-0", []byte("again"), t0); err == nil {
		t.Fatal("a second result was accepted")
	}
	sameAsUnacked(t, db, "after a second result for c-0 (refused)")

	if n, err := db.AckResults(ctx, []string{"c-3", "c-unknown", "c-running", "c-3"}, now); err != nil || n != 1 {
		t.Fatalf("AckResults = %d, %v; want 1", n, err)
	}
	sameAsUnacked(t, db, "after an ack (one real id among a stranger, a running one and a repeat)")
	if got := db.UnackedCount(); got != 3 {
		t.Errorf("UnackedCount = %d after the ack, want 3", got)
	}

	// c-0 and c-1 are unacked and past their deadline: Prune deletes them, and
	// the count follows. c-2 is unacked inside its deadline and stays.
	// It also deletes the acked c-3 and the unfinished c-running, whose
	// deadlines are past: four rows, two of them unacked.
	if n, err := db.Prune(ctx, now); err != nil || n != 4 {
		t.Fatalf("Prune = %d, %v; want 4 (c-0, c-1, c-3 and c-running)", n, err)
	}
	sameAsUnacked(t, db, "after Prune removed unacked rows")
	if got := db.UnackedCount(); got != 1 {
		t.Errorf("UnackedCount = %d after Prune, want 1 (c-2)", got)
	}

	// A restart: Open seeds the count from the file.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2 := open(t, dir)
	sameAsUnacked(t, db2, "after reopening the file")
	if got := db2.UnackedCount(); got != 1 {
		t.Errorf("UnackedCount after reopen = %d, want 1", got)
	}
}

// Many results, acks and prunes at once through the one writer: the count is
// exact when they are done (go test -race).
func TestUnackedCountIsExactUnderConcurrentWriters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	const n = 120
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			c := command(fmt.Sprintf("c-%03d", i), 1, uint64(i+1)) //nolint:gosec // G115: i is below 120
			if _, _, err := db.LogCommand(ctx, c); err != nil {
				t.Error(err)
				return
			}
			if err := db.RecordResult(ctx, c.ID, []byte("r"), t0); err != nil {
				t.Error(err)
				return
			}
			if i%3 == 0 {
				if _, err := db.AckResults(ctx, []string{c.ID}, t0); err != nil {
					t.Error(err)
				}
			}
			if i%10 == 0 {
				if _, err := db.Prune(ctx, t0.Add(time.Hour)); err != nil { // c.NotAfter is a minute after t0: past
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	sameAsUnacked(t, db, "after concurrent writers")
}

// The executor's writes (LogAndApply, SetRunning, a refused SetRunning on a
// finished entry, a repeated LogAndApply) log and mark entries that have no
// result yet, so they never move the count; only the result that follows
// does. Checked step by step against Unacked().
func TestUnackedCountIgnoresLogAndApplyAndSetRunning(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	if err := db.StartEpoch(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, dup, err := db.LogAndApply(ctx, command(fmt.Sprintf("c-%d", i), 2, uint64(i+1))); err != nil || dup {
			t.Fatalf("LogAndApply c-%d: dup %v, err %v", i, dup, err)
		}
		sameAsUnacked(t, db, fmt.Sprintf("after LogAndApply c-%d", i))
	}
	if got := db.UnackedCount(); got != 0 {
		t.Fatalf("UnackedCount = %d with three commands and no result, want 0", got)
	}
	if err := db.SetRunning(ctx, "c-0", 2); err != nil {
		t.Fatal(err)
	}
	sameAsUnacked(t, db, "after SetRunning")
	if _, dup, err := db.LogAndApply(ctx, command("c-0", 2, 9)); err != nil || !dup {
		t.Fatalf("a repeated LogAndApply: dup %v, err %v; want dup", dup, err)
	}
	sameAsUnacked(t, db, "after a repeated LogAndApply")

	if err := db.RecordResult(ctx, "c-0", []byte("r"), t0); err != nil {
		t.Fatal(err)
	}
	sameAsUnacked(t, db, "after the result of a running command")
	if got := db.UnackedCount(); got != 1 {
		t.Fatalf("UnackedCount = %d after one result, want 1", got)
	}
	if err := db.SetRunning(ctx, "c-0", 3); !errors.Is(err, ring.ErrResultLogged) {
		t.Fatalf("SetRunning on a finished entry = %v, want ErrResultLogged", err)
	}
	sameAsUnacked(t, db, "after a refused SetRunning")
	opened, err := db.Open(ctx)
	if err != nil || len(opened) != 2 {
		t.Fatalf("Open = %d entries, %v; want 2 (c-1 and c-2)", len(opened), err)
	}
	sameAsUnacked(t, db, "after Open")
}
