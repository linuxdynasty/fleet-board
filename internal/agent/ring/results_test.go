package ring_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// finished logs a command received at at and records its result.
func finished(t *testing.T, db *ring.DB, id string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	c := command(id, 1, 1)
	c.Received = at
	if _, _, err := db.LogCommand(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordResult(ctx, id, []byte("result-"+id), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func ids(es []ring.Entry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

// 02i: a result stays unacked, and is re-sent after every Snapshot, until
// ResultAck; an ack for an unknown, unfinished or already acked id changes
// nothing, and an acked result is still answered from the log.
func TestResultsStayUnackedUntilResultAck(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	ctx := context.Background()
	finished(t, db, "c-2", t0.Add(time.Minute))
	finished(t, db, "c-1", t0)
	if _, _, err := db.LogCommand(ctx, command("c-running", 1, 3)); err != nil {
		t.Fatal(err)
	}

	un, err := db.Unacked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ids(un), ","); got != "c-1,c-2" {
		t.Errorf("Unacked = %s, want c-1,c-2 (finished only, oldest first)", got)
	}

	n, err := db.AckResults(ctx, []string{"c-1", "c-unknown", "c-running"}, t0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("AckResults = %d, %v; want 1 (c-1 only)", n, err)
	}
	if n, err := db.AckResults(ctx, []string{"c-1"}, t0.Add(2*time.Hour)); err != nil || n != 0 {
		t.Errorf("a repeated ack = %d, %v; want 0", n, err)
	}
	if n, err := db.AckResults(ctx, nil, t0); err != nil || n != 0 {
		t.Errorf("an empty ack = %d, %v; want 0", n, err)
	}
	un, err = db.Unacked(ctx)
	if err != nil || strings.Join(ids(un), ",") != "c-2" {
		t.Errorf("Unacked after the ack = %v, %v; want c-2", ids(un), err)
	}
	e, err := db.Command(ctx, "c-1")
	if err != nil || string(e.Result) != "result-c-1" || !e.Acked.Equal(t0.Add(time.Hour)) {
		t.Errorf("c-1 after its ack = %+v, %v; want the logged result, acked at the first ack", e, err)
	}
	running, err := db.Command(ctx, "c-running")
	if err != nil || !running.Acked.IsZero() {
		t.Errorf("an ack for a running command stamped it: %+v, %v", running, err)
	}
}

// 02i, defaults.md: the log keeps the last 10,000 entries or 7 days.
func TestPruneKeepsSevenDaysAndTenThousandEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := t0.Add(30 * 24 * time.Hour)
	t.Run("by age", func(t *testing.T) {
		t.Parallel()
		db := open(t, stateDir(t))
		finished(t, db, "old", now.Add(-7*24*time.Hour-time.Second))
		finished(t, db, "edge", now.Add(-7*24*time.Hour))
		finished(t, db, "new", now.Add(-time.Hour))
		n, err := db.Prune(ctx, now)
		if err != nil || n != 1 {
			t.Fatalf("Prune = %d, %v; want 1", n, err)
		}
		if _, err := db.Command(ctx, "old"); err == nil {
			t.Error("an entry older than 7 days was kept")
		}
		for _, id := range []string{"edge", "new"} {
			if _, err := db.Command(ctx, id); err != nil {
				t.Errorf("%s: %v, want kept", id, err)
			}
		}
	})
	t.Run("by count", func(t *testing.T) {
		t.Parallel()
		db := open(t, stateDir(t))
		const total = ring.MaxCommands + 3
		logMany(t, db, total, now, nil)
		n, err := db.Prune(ctx, now)
		if err != nil || n != 3 {
			t.Fatalf("Prune = %d, %v; want 3", n, err)
		}
		for i, want := range map[int]bool{0: false, 2: false, 3: true, total - 1: true} {
			_, err := db.Command(ctx, fmt.Sprintf("c-%05d", i))
			if kept := err == nil; kept != want {
				t.Errorf("entry %d kept = %v, want %v", i, kept, want)
			}
		}
	})
}

// logMany logs total commands received an hour before now, 1 ms apart, all
// past their deadline unless set changes them.
func logMany(t *testing.T, db *ring.DB, total int, now time.Time, set func(i int, c *ring.Command)) {
	t.Helper()
	ctx := context.Background()
	// Many writers at once, so the one writer batches them (an fsync per
	// entry would take minutes).
	var wg sync.WaitGroup
	next := make(chan int)
	for range 100 {
		wg.Go(func() {
			for i := range next {
				c := command(fmt.Sprintf("c-%05d", i), 1, uint64(i+1)) //nolint:gosec // G115: i is below 10,003
				c.Received = now.Add(-time.Hour + time.Duration(i)*time.Millisecond)
				if set != nil {
					set(i, &c)
				}
				if _, _, err := db.LogCommand(ctx, c); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for i := range total {
		next <- i
	}
	close(next)
	wg.Wait()
}

// The hub re-sends an open command until its not_after_time (up to 24 h),
// so the count cap never forgets one inside its deadline that is not acked:
// the re-sent command is a duplicate, never a second run (principle 2).
func TestTheCountCapKeepsCommandsTheHubMayResend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := t0.Add(30 * 24 * time.Hour)
	db := open(t, stateDir(t))
	const total = ring.MaxCommands + 3
	logMany(t, db, total, now, func(i int, c *ring.Command) {
		if i < 2 {
			c.NotAfter = now.Add(time.Hour)
		}
	})
	if err := db.RecordResult(ctx, "c-00001", []byte("r"), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	n, err := db.Prune(ctx, now)
	if err != nil || n != 3 {
		t.Fatalf("Prune = %d, %v; want 3", n, err)
	}
	for i, want := range map[int]bool{0: true, 1: true, 2: false, 4: false, 5: true, total - 1: true} {
		_, err := db.Command(ctx, fmt.Sprintf("c-%05d", i))
		if kept := err == nil; kept != want {
			t.Errorf("entry %d kept = %v, want %v", i, kept, want)
		}
	}
	again := command("c-00000", 2, 1)
	again.Received, again.NotAfter = now, now.Add(time.Hour)
	if _, dup, err := db.LogCommand(ctx, again); err != nil || !dup {
		t.Errorf("the hub's re-send of a kept command: dup %v, err %v; want a duplicate", dup, err)
	}
	if n, err := db.Prune(ctx, now); err != nil || n != 0 {
		t.Errorf("a second Prune = %d, %v; want nothing left to prune", n, err)
	}
}

// The file backs the ack rule: an ack time never changes once set, and only
// a finished entry can carry one.
func TestTheFileKeepsTheFirstAck(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	ctx := context.Background()
	finished(t, db, "c-1", t0)
	if _, _, err := db.LogCommand(ctx, command("c-2", 1, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AckResults(ctx, []string{"c-1"}, t0); err != nil {
		t.Fatal(err)
	}
	f := raw(t, dir)
	for _, q := range []string{
		`UPDATE command_log SET acked_time = NULL WHERE command_id = 'c-1'`,
		`UPDATE command_log SET acked_time = acked_time + 1 WHERE command_id = 'c-1'`,
		`UPDATE command_log SET acked_time = 5 WHERE command_id = 'c-2'`,
		`INSERT INTO command_log(command_id, kind, epoch, seq, received_time, not_after_time, status, acked_time)
			VALUES ('c-3', 'capture_pane', 1, 3, 0, 0, 'received', 5)`,
	} {
		if _, err := f.ExecContext(ctx, q); err == nil || !strings.Contains(err.Error(), "ack") {
			t.Errorf("%q gave %v, want a refusal naming the ack", q, err)
		}
	}
}

// The age test compares hub time with received_time, which the agent's
// clock stamps. With that clock 8 days behind the hub, every entry looks
// older than 7 days, so the age limit too spares an entry that is not
// acked and inside its deadline (hub time): the hub may still re-send it,
// and a forgotten command_id would run twice. Acked entries and ones past
// their deadline still go.
func TestTheAgeLimitKeepsCommandsTheHubMayResend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	hub := t0.Add(30 * 24 * time.Hour)
	agentNow := hub.Add(-8 * 24 * time.Hour) // the agent's clock, 8 days behind
	for _, e := range []struct {
		id       string
		notAfter time.Time
		acked    bool
	}{
		{"open", hub.Add(time.Hour), false},
		{"acked", hub.Add(time.Hour), true},
		{"expired", hub.Add(-time.Minute), false},
	} {
		c := command(e.id, 1, 1)
		c.Received, c.NotAfter = agentNow, e.notAfter
		if _, _, err := db.LogCommand(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := db.RecordResult(ctx, e.id, []byte("result"), agentNow.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if e.acked {
			if _, err := db.AckResults(ctx, []string{e.id}, hub); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, err := db.Prune(ctx, hub)
	if err != nil || n != 2 {
		t.Fatalf("Prune = %d, %v; want 2 (the acked and the expired entry)", n, err)
	}
	if _, err := db.Command(ctx, "open"); err != nil {
		t.Errorf("the unacked entry inside its deadline was pruned by age: %v", err)
	}
	for _, id := range []string{"acked", "expired"} {
		if _, err := db.Command(ctx, id); err == nil {
			t.Errorf("%s: kept, want pruned (older than 7 days and forgettable)", id)
		}
	}
}
