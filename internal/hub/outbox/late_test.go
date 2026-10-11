package outbox_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// Enqueue refuses a REVOKED machine and writes nothing (round 3 ruling,
// 2b: 02g "nothing is sent to it again"), but a command_id already stored
// is still the no-op that returns the stored row.
func TestEnqueueRefusesARevokedMachine(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	first := mustEnqueue(t, s, "m1", "c1")
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) (int, error) {
		_, err := tx.ExecContext(ctx, `UPDATE machines SET state = 'REVOKED' WHERE machine_id = 'm1'`)
		return 0, err
	})
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: interrupt("c2"), Now: t0}); !errors.Is(err, outbox.ErrMachineRevoked) {
		t.Errorf("Enqueue for a revoked machine = %v, want ErrMachineRevoked", err)
	}
	if n := count(t, s, `SELECT count(*) FROM commands`); n != 1 {
		t.Errorf("%d rows, want only c1", n)
	}
	again, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: interrupt("c1"), Now: t0})
	if err != nil || again.Created || again.HubSeq != first.HubSeq {
		t.Errorf("a repeat of c1 = %+v, %v; want the stored row", again, err)
	}
}

// A CommandResult for a row the hub closed with no result (expired by its
// deadline, or closed by a revoke) stays Known (so it is acked) and is
// stored nowhere, but it is logged (outbox.late_result) and counted with
// outcome late (round 3 ruling, 2: "the Operation says EXPIRED; did it
// run?").
func TestALateResultIsAckedLoggedCountedAndNotStored(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	clk := &clock{t: t0}
	h := newService(t, s, newFakeStreams(), clk.now)
	enqueueDue(t, s, "m1", "c-expired", t0.Add(time.Minute))
	enqueueDue(t, s, "m2", "c-revoked", t0.Add(time.Hour))
	closedAt := t0.Add(2 * time.Minute)
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		if _, err := outbox.Expire(ctx, tx, "m1", closedAt, 10); err != nil {
			return nil, err
		}
		return outbox.CloseForRevoke(ctx, tx, "m2", closedAt)
	})
	clk.set(closedAt.Add(5 * time.Second))
	for _, tc := range []struct{ machine, id string }{{"m1", "c-expired"}, {"m2", "c-revoked"}} {
		before, _ := readRow(t, s, tc.id)
		res := resultOf(tc.id, typev1.Status_STATUS_FAILED, "it ran late")
		res.Reason = typev1.ReasonCode_REASON_CODE_PANE_BUSY
		out, err := h.svc.Result(context.Background(), outbox.From{MachineID: tc.machine, Epoch: 1, Live: true}, res)
		if err != nil || out != outbox.Known {
			t.Errorf("%s: Result = %v, %v; want Known", tc.id, out, err)
		}
		if after, _ := readRow(t, s, tc.id); after.version != before.version || after.state != before.state ||
			after.status != "EXPIRED" {
			t.Errorf("%s changed: %+v -> %+v", tc.id, before, after)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM command_results`); n != 0 {
		t.Errorf("%d late results stored", n)
	}
	ev := h.events(t, "outbox.late_result")
	if len(ev) != 2 {
		t.Fatalf("outbox.late_result %v, want 2", ev)
	}
	for _, e := range ev {
		if e["kind"] != "interrupt" || e["status"] != "FAILED" || e["reason"] != "PANE_BUSY" ||
			e["late_ms"] != float64(5000) || (e["command_id"] != "c-expired" && e["command_id"] != "c-revoked") {
			t.Errorf("outbox.late_result %v", e)
		}
	}
	body := h.scrape(t)
	if v := sample(body, "fleet_hub_command_messages_total", `arm="command_result"`, `outcome="late"`); v != 2 {
		t.Errorf("command_messages_total{outcome=late} = %v, want 2", v)
	}
	if v := sample(body, "fleet_hub_command_messages_total", `arm="command_result"`, `outcome="known"`); v != -1 {
		t.Errorf("command_messages_total{outcome=known} = %v, want none: a late result is not a repeat", v)
	}
}
