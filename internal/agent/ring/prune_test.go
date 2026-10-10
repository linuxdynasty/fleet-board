package ring_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// 02i "Clocks": a deadline is hub time, so Prune takes hub time
// (HelloAck.hub_time plus the time since) and never reads the agent's wall
// clock. Here the agent's clock runs 2 h ahead of the hub's: the oldest
// entry's not_after_time is an hour after the hub's now but an hour before
// the agent's, so the hub may still re-send it and the count cap keeps it.
func TestPruneTakesHubTimeNotTheWallClock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const skew = 2 * time.Hour
	hubNow := time.Now().Add(-skew)
	db := open(t, stateDir(t))
	const total = ring.MaxCommands + 1
	logMany(t, db, total, hubNow, func(i int, c *ring.Command) {
		c.Received = c.Received.Add(skew) // received_time is the agent's clock
		c.NotAfter = hubNow.Add(-time.Minute)
		if i == 0 {
			c.NotAfter = hubNow.Add(time.Hour)
		}
	})
	n, err := db.Prune(ctx, hubNow)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1 (c-00001, past its deadline in hub time)", n, err)
	}
	for i, want := range map[int]bool{0: true, 1: false, 2: true, total - 1: true} {
		_, err := db.Command(ctx, fmt.Sprintf("c-%05d", i))
		if kept := err == nil; kept != want {
			t.Errorf("entry %d kept = %v, want %v", i, kept, want)
		}
	}
}

// An acked entry is never re-sent by the hub, so past 10,000 entries the
// count cap forgets it even while it is inside its deadline.
func TestTheCountCapForgetsAnAckedCommandInsideItsDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := t0.Add(30 * 24 * time.Hour)
	db := open(t, stateDir(t))
	const total = ring.MaxCommands + 1
	logMany(t, db, total, now, func(_ int, c *ring.Command) {
		c.NotAfter = now.Add(time.Hour)
	})
	if err := db.RecordResult(ctx, "c-00000", []byte("r"), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := db.AckResults(ctx, []string{"c-00000"}, now); err != nil || n != 1 {
		t.Fatalf("AckResults = %d, %v; want 1", n, err)
	}
	n, err := db.Prune(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v; want 1 (the acked c-00000)", n, err)
	}
	for i, want := range map[int]bool{0: false, 1: true, total - 1: true} {
		_, err := db.Command(ctx, fmt.Sprintf("c-%05d", i))
		if kept := err == nil; kept != want {
			t.Errorf("entry %d kept = %v, want %v", i, kept, want)
		}
	}
}

// A result recorded before the agent stops (or crashes) is still unacked
// after a reopen, so the next stream re-sends it, and its ack then lands.
func TestAnUnackedResultSurvivesAReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := stateDir(t)
	first, err := ring.Open(ctx, dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	finished(t, first, "c-1", t0)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	db := open(t, dir)
	un, err := db.Unacked(ctx)
	if err != nil || len(un) != 1 || un[0].ID != "c-1" || string(un[0].Result) != "result-c-1" {
		t.Fatalf("Unacked after a reopen = %v, %v; want c-1 with its result", ids(un), err)
	}
	if n, err := db.AckResults(ctx, []string{"c-1"}, t0.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("AckResults after a reopen = %d, %v; want 1", n, err)
	}
	if un, err := db.Unacked(ctx); err != nil || len(un) != 0 {
		t.Errorf("Unacked after the ack = %v, %v; want none", ids(un), err)
	}
}
