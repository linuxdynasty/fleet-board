package outbox_test

import (
	"context"
	"database/sql"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// clock is a hub clock a test moves.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }

// enqueueDue enqueues an interrupt for machine due at due.
func enqueueDue(t *testing.T, s *store.Store, machine, id string, due time.Time) outbox.Op {
	t.Helper()
	c := interrupt(id)
	c.NotAfterTime = timestamppb.New(due)
	op, err := enqueue(t, s, outbox.In{MachineID: machine, Command: c, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// writeJob runs fn in one writer job.
func writeJob[T any](t *testing.T, s *store.Store, fn func(ctx context.Context, tx *sql.Tx) (T, error)) T {
	t.Helper()
	var out T
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		out, err = fn(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func closedIDs(list []outbox.Closed) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.CommandID)
	}
	slices.Sort(out)
	return out
}

// Expire, in the caller's writer job, closes the machine's pending and
// delivered rows whose deadline has come (02i: pending --> expired,
// delivered --> expired): state expired, Operation DONE with status and
// reason EXPIRED, done_time now, and each row its own version and
// hub_seq. A row due later, a closed row and another machine's row are
// left alone; "" means every machine; limit bounds one call.
func TestExpireClosesOpenRowsPastTheirDeadline(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), func() time.Time { return t0 })
	due := t0.Add(time.Hour)
	p := enqueueDue(t, s, "m1", "c-pending", due)
	enqueueDue(t, s, "m1", "c-delivered", due)
	enqueueDue(t, s, "m1", "c-done", due)
	enqueueDue(t, s, "m1", "c-later", due.Add(time.Minute))
	enqueueDue(t, s, "m2", "c-other", due)
	from := outbox.From{MachineID: "m1", Epoch: 1, Live: true}
	if _, err := h.svc.Ack(context.Background(), from, ackOf("c-delivered")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Result(context.Background(), from, resultOf("c-done", typev1.Status_STATUS_OK, "")); err != nil {
		t.Fatal(err)
	}
	dRow, _ := readRow(t, s, "c-delivered")

	got := writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.Expire(ctx, tx, "m1", due, 100)
	})
	if want := []string{"c-delivered", "c-pending"}; !slices.Equal(closedIDs(got), want) {
		t.Fatalf("Expire closed %v, want %v", closedIDs(got), want)
	}
	for _, c := range got {
		wantFrom := map[string]string{"c-pending": "pending", "c-delivered": "delivered"}[c.CommandID]
		if c.MachineID != "m1" || c.Kind != "interrupt" || c.From != wantFrom || c.Cause != outbox.CauseDeadline ||
			!c.Created.Equal(t0) {
			t.Errorf("Closed %+v", c)
		}
	}
	var seqs []int64
	for id, before := range map[string]row{"c-pending": {version: p.Version, hubSeq: p.HubSeq}, "c-delivered": dRow} {
		r, _ := readRow(t, s, id)
		if r.state != "expired" || r.opState != "DONE" || r.status != "EXPIRED" || r.reason != "EXPIRED" ||
			!r.doneTime.Valid || r.doneTime.Int64 != due.UnixMilli() || r.version != before.version+1 ||
			r.hubSeq <= before.hubSeq {
			t.Errorf("%s after Expire: %+v", id, r)
		}
		seqs = append(seqs, r.hubSeq)
	}
	if seqs[0] == seqs[1] {
		t.Errorf("both rows took hub_seq %d: each changed resource takes its own", seqs[0])
	}
	for id, state := range map[string]string{"c-done": "done", "c-later": "pending", "c-other": "pending"} {
		if r, _ := readRow(t, s, id); r.state != state {
			t.Errorf("%s is %s, want %s", id, r.state, state)
		}
	}
	if again := writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.Expire(ctx, tx, "m1", due, 100)
	}); len(again) != 0 {
		t.Errorf("a second Expire closed %v", closedIDs(again))
	}
	all := writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.Expire(ctx, tx, "", due.Add(time.Minute), 1)
	})
	if len(all) != 1 {
		t.Errorf("Expire with limit 1 closed %v", closedIDs(all))
	}
	if n := count(t, s, `SELECT count(*) FROM commands WHERE state IN ('pending', 'delivered')`); n != 1 {
		t.Errorf("%d open rows left, want 1 (the limit held back one)", n)
	}
}

// A revoke closes every open row of the machine in the revoke's job,
// whatever its deadline: state done, Operation DONE, status EXPIRED,
// reason MACHINE_REVOKED (02i: pending/delivered --> done on RevokeMachine;
// 02g "Revoke, in one transaction"). Closed rows and other machines stay.
func TestCloseForRevokeClosesTheMachinesOpenRows(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), func() time.Time { return t0 })
	for _, id := range []string{"c-pending", "c-delivered", "c-done"} {
		enqueueDue(t, s, "m1", id, t0.Add(time.Hour))
	}
	enqueueDue(t, s, "m2", "c-other", t0.Add(time.Hour))
	from := outbox.From{MachineID: "m1", Epoch: 1, Live: true}
	if _, err := h.svc.Ack(context.Background(), from, ackOf("c-delivered")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Result(context.Background(), from, resultOf("c-done", typev1.Status_STATUS_OK, "")); err != nil {
		t.Fatal(err)
	}
	at := t0.Add(time.Minute)
	got := writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.CloseForRevoke(ctx, tx, "m1", at)
	})
	if want := []string{"c-delivered", "c-pending"}; !slices.Equal(closedIDs(got), want) {
		t.Fatalf("CloseForRevoke closed %v, want %v", closedIDs(got), want)
	}
	for _, c := range got {
		if c.Cause != outbox.CauseRevoked {
			t.Errorf("Closed %+v, want cause revoked", c)
		}
		r, _ := readRow(t, s, c.CommandID)
		if r.state != "done" || r.opState != "DONE" || r.status != "EXPIRED" || r.reason != "MACHINE_REVOKED" ||
			r.doneTime.Int64 != at.UnixMilli() {
			t.Errorf("%s after revoke: %+v", c.CommandID, r)
		}
	}
	if r, _ := readRow(t, s, "c-done"); r.status != "OK" {
		t.Errorf("c-done became %+v", r)
	}
	if r, _ := readRow(t, s, "c-other"); r.state != "pending" {
		t.Errorf("m2's row became %s", r.state)
	}
}

// The housekeeping job expires what is due on every machine, logs each
// row (outbox.expired) and counts it by kind and cause, after the commit.
// The sender never sends an expired row, and a late result for it is
// answered (Known) but not stored: 03n "Results for commands the hub
// already closed are ignored".
func TestExpireDueLogsCountsAndIgnoresALateResult(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	clk := &clock{t: t0}
	streams := newFakeStreams()
	h := newService(t, s, streams, clk.now)
	enqueueDue(t, s, "m1", "c1", t0.Add(time.Hour))
	enqueueDue(t, s, "m2", "c2", t0.Add(time.Hour))
	enqueueDue(t, s, "m2", "c3", t0.Add(2*time.Hour))
	at := t0.Add(time.Hour + time.Second)
	clk.set(at)
	if err := h.svc.ExpireDue(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	ev := h.events(t, "outbox.expired")
	if len(ev) != 2 {
		t.Fatalf("outbox.expired lines %v, want 2", ev)
	}
	for _, e := range ev {
		if e["kind"] != "interrupt" || e["cause"] != "deadline" || e["from"] != "pending" ||
			e["age_ms"] != float64(time.Hour.Milliseconds()+1000) || (e["command_id"] != "c1" && e["command_id"] != "c2") {
			t.Errorf("outbox.expired %v", e)
		}
	}
	if v := sample(h.scrape(t), "fleet_hub_commands_expired_total", `kind="interrupt"`, `cause="deadline"`); v != 2 {
		t.Errorf("fleet_hub_commands_expired_total = %v, want 2", v)
	}

	streams.setLive("m2", 1)
	h.svc.Ready("m2", 1)
	got := streams.waitSent(t, 1)
	if len(got) != 1 || got[0].cmd.GetCommandId().GetValue() != "c3" {
		t.Errorf("sent %v, want only c3", got)
	}
	out, err := h.svc.Result(context.Background(), outbox.From{MachineID: "m1", Epoch: 1, Live: true}, resultOf("c1", typev1.Status_STATUS_OK, ""))
	if err != nil || out != outbox.Known {
		t.Errorf("late result = %v, %v; want Known", out, err)
	}
	if n := count(t, s, `SELECT count(*) FROM command_results`); n != 0 {
		t.Errorf("%d results stored for an expired command", n)
	}
	if r, _ := readRow(t, s, "c1"); r.state != "expired" {
		t.Errorf("c1 is %s after a late result", r.state)
	}
}

// A row the sender read inside its deadline, then expired by housekeeping
// before the sender recorded the send, keeps an honest sent stamp (it was
// queued; a send is not a state), stays expired, and takes no version or
// hub_seq bump from the mark (03f race table: the agent refuses it
// EXPIRED and its result is dropped as late).
func TestASendRecordedAfterExpiryStampsOnlyTheSend(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), func() time.Time { return t0 })
	due := t0.Add(time.Minute)
	enqueueDue(t, s, "m1", "c1", due)
	writeJob(t, s, func(ctx context.Context, tx *sql.Tx) ([]outbox.Closed, error) {
		return outbox.Expire(ctx, tx, "m1", due, 10)
	})
	before, _ := readRow(t, s, "c1")
	outbox.MarkSent(h.svc, "m1", "c1", 3, 1)
	after, _ := readRow(t, s, "c1")
	if !after.sentEpoch.Valid || after.sentEpoch.Int64 != 3 || after.sentSeq.Int64 != 1 {
		t.Errorf("sent stamp %v/%v, want epoch 3 seq 1: the command was queued", after.sentEpoch, after.sentSeq)
	}
	if after.state != "expired" || after.opState != "DONE" || after.version != before.version || after.hubSeq != before.hubSeq {
		t.Errorf("row %+v after the mark, was %+v: a send changes no state", after, before)
	}
}
