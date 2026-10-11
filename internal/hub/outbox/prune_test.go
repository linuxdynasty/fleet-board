package outbox_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// Retention is 30 days after a command closed (03f data model, defaults).
func TestRetentionIsThirtyDays(t *testing.T) {
	t.Parallel()
	if outbox.Retention != 30*24*time.Hour {
		t.Errorf("Retention = %v, want 30 days", outbox.Retention)
	}
}

// Prune deletes the rows that closed more than Retention ago, their
// stored results with them, in bounded jobs; a row closed later, and an
// open row however old, stay. It logs and counts what it removed.
func TestPruneRemovesClosedRowsPastRetention(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	clk := &clock{t: t0}
	h := newService(t, s, newFakeStreams(), clk.now)
	from := outbox.From{MachineID: "m1", Epoch: 1, Live: true}
	for _, id := range []string{"c-old-done", "c-old-expired", "c-open", "c-new-done"} {
		enqueueDue(t, s, "m1", id, t0.Add(time.Hour))
	}
	if _, err := h.svc.Result(context.Background(), from, resultOf("c-old-done", typev1.Status_STATUS_OK, "")); err != nil {
		t.Fatal(err)
	}
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		_, err := tx.ExecContext(ctx, `UPDATE commands SET not_after_time = ? WHERE command_id = 'c-old-expired'`,
			t0.Add(time.Minute).UnixMilli())
		if err != nil {
			return nil, err
		}
		return outbox.Expire(ctx, tx, "m1", t0.Add(time.Minute), 10)
	})
	newer := t0.Add(2 * time.Hour)
	clk.set(newer)
	if _, err := h.svc.Result(context.Background(), from, resultOf("c-new-done", typev1.Status_STATUS_OK, "")); err != nil {
		t.Fatal(err)
	}

	at := newer.Add(outbox.Retention - time.Minute) // past the old rows' 30 days, not the newer one's
	if err := h.svc.Prune(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"c-old-done": false, "c-old-expired": false, "c-open": true, "c-new-done": true} {
		if _, ok := readRow(t, s, id); ok != want {
			t.Errorf("%s kept = %v, want %v", id, ok, want)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM command_results`); n != 1 {
		t.Errorf("%d results left, want the newer one only", n)
	}
	ev := h.events(t, "outbox.pruned")
	if len(ev) != 1 || ev[0]["commands"] != float64(2) {
		t.Errorf("outbox.pruned %v, want one line with commands 2", ev)
	}
	if v := sample(h.scrape(t), "fleet_hub_outbox_pruned_total"); v != 2 {
		t.Errorf("fleet_hub_outbox_pruned_total = %v, want 2", v)
	}
	if err := h.svc.Prune(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if n := len(h.events(t, "outbox.pruned")); n != 1 {
		t.Errorf("a prune with nothing to remove logged (%d lines)", n)
	}
}
