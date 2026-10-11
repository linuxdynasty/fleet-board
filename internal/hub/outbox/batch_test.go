package outbox_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// enqueueMany enqueues n interrupts for m1, named prefix-0 to prefix-n-1,
// due at due, in one writer job.
func enqueueBatch(t *testing.T, s *store.Store, prefix string, n int, due time.Time) {
	t.Helper()
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) (int, error) {
		for i := range n {
			c := interrupt(fmt.Sprintf("%s-%d", prefix, i))
			c.NotAfterTime = timestamppb.New(due)
			if _, err := outbox.Enqueue(ctx, tx, outbox.In{MachineID: "m1", Command: c, Now: t0}); err != nil {
				return 0, err
			}
		}
		return n, nil
	})
}

// ExpireDue goes on past one job's 500 rows until nothing due is left: 501
// due rows all close, each logged once, and a row due later stays open.
func TestExpireDueLoopsPastOneJob(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), func() time.Time { return t0 })
	due := t0.Add(time.Minute)
	enqueueBatch(t, s, "due", 501, due)
	enqueueDue(t, s, "m1", "later", due.Add(time.Hour))
	if err := h.svc.ExpireDue(context.Background(), due); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands WHERE state = 'expired'`); n != 501 {
		t.Errorf("%d expired, want 501", n)
	}
	if r, _ := readRow(t, s, "later"); r.state != "pending" {
		t.Errorf("the row due later is %s", r.state)
	}
	if n := len(h.events(t, "outbox.expired")); n != 501 {
		t.Errorf("%d outbox.expired lines, want 501", n)
	}
}

// Prune goes on past one job's 1,000 rows: 1,001 rows closed more than 30
// days ago all go, while open rows enqueued just as long ago stay.
func TestPruneLoopsPastOneJobAndKeepsOpenRows(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), func() time.Time { return t0 })
	due := t0.Add(time.Minute)
	enqueueBatch(t, s, "closed", 1001, due)
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.Expire(ctx, tx, "m1", due, 2000)
	})
	enqueueBatch(t, s, "open", 3, due.Add(outbox.Retention*2))
	if err := h.svc.Prune(context.Background(), due.Add(outbox.Retention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands WHERE state = 'expired'`); n != 0 {
		t.Errorf("%d closed rows left, want 0", n)
	}
	if n := count(t, s, `SELECT count(*) FROM commands WHERE state = 'pending'`); n != 3 {
		t.Errorf("%d open rows left, want 3", n)
	}
	if ev := h.events(t, "outbox.pruned"); len(ev) != 1 || ev[0]["commands"] != float64(1001) {
		t.Errorf("outbox.pruned %v, want one line with 1001", ev)
	}
}
