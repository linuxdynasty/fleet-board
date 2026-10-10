package outbox_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// waitMarked waits until id's send on epoch with seq is recorded.
func waitMarked(t *testing.T, s *store.Store, id string, epoch, seq int64) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		r, _ := readRow(t, s, id)
		if r.sentEpoch.Int64 == epoch && r.sentSeq.Int64 == seq {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("%s sent epoch %v seq %v, want %d %d", id, r.sentEpoch, r.sentSeq, epoch, seq)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func ids(got []sent) []string {
	out := make([]string, len(got))
	for i, g := range got {
		out[i] = g.cmd.GetCommandId().GetValue()
	}
	return out
}

func sameIDs(a []string, b ...string) bool { return slices.Equal(a, b) }

// Once a machine is ready, its pending commands go out on that epoch's
// stream in enqueue order with seqs 1, 2, 3, and each send is recorded;
// another machine's command stays where it is.
func TestReadySendsPendingCommandsInOrderWithSeqsFromOne(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 5)
	streams.setLive("m2", 9)
	h := newService(t, s, streams, fixedClock(t0))
	for _, id := range []string{"c1", "c2", "c3"} {
		mustEnqueue(t, s, "m1", id)
	}
	mustEnqueue(t, s, "m2", "other")
	h.svc.Ready("m1", 5)
	got := streams.waitSent(t, 3)
	if !sameIDs(ids(got), "c1", "c2", "c3") {
		t.Fatalf("sent %v, want c1 c2 c3", ids(got))
	}
	for i, g := range got {
		if g.machine != "m1" || g.epoch != 5 || g.cmd.GetEpoch() != 5 || g.cmd.GetSeq() != uint64(i+1) {
			t.Errorf("send %d: machine %s stream epoch %d, command epoch %d seq %d", i, g.machine, g.epoch,
				g.cmd.GetEpoch(), g.cmd.GetSeq())
		}
		waitMarked(t, s, g.cmd.GetCommandId().GetValue(), 5, int64(i+1))
	}
	if r, _ := readRow(t, s, "c1"); r.state != "pending" || r.opState != "PENDING" || r.version != 1 {
		t.Errorf("a send is not an Operation change: %+v", r)
	}
	if r, _ := readRow(t, s, "other"); r.sentEpoch.Valid {
		t.Errorf("m2's command was sent: %+v", r)
	}
	if v := sample(h.scrape(t), "fleet_hub_outbox_sends_total", `result="queued"`); v != 3 {
		t.Errorf("fleet_hub_outbox_sends_total{queued} = %v, want 3", v)
	}
	if lines := h.events(t, "outbox.sent"); len(lines) != 3 || lines[2]["seq"] != float64(3) || lines[2]["epoch"] != float64(5) {
		t.Errorf("outbox.sent lines %v", lines)
	}
}

// Nothing goes to a machine before its Snapshot of the epoch committed
// (Ready), even with a live stream and a Notify.
func TestNothingIsSentBeforeReady(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	streams.setLive("m2", 1)
	h := newService(t, s, streams, fixedClock(t0))
	h.svc.Notify(mustEnqueue(t, s, "m1", "c1"))
	h.svc.Notify(mustEnqueue(t, s, "m2", "c2"))
	h.svc.Ready("m2", 1)
	streams.waitSent(t, 1)
	waitMarked(t, s, "c2", 1, 1) // the pass that sent c2 has ended
	if got := ids(streams.got()); !sameIDs(got, "c2") {
		t.Errorf("sent %v, want only c2", got)
	}
	if lines := h.events(t, "outbox.enqueued"); len(lines) != 2 {
		t.Errorf("outbox.enqueued lines %v, want 2", lines)
	}
}

// A command enqueued for a ready machine goes out on Notify, after what
// went before it on the epoch.
func TestNotifySendsToAReadyMachine(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 2)
	h := newService(t, s, streams, fixedClock(t0))
	h.svc.Ready("m1", 2)
	h.svc.Notify(mustEnqueue(t, s, "m1", "c1"))
	streams.waitSent(t, 1)
	h.svc.Notify(mustEnqueue(t, s, "m1", "c2"))
	got := streams.waitSent(t, 2)
	if !sameIDs(ids(got), "c1", "c2") || got[1].cmd.GetSeq() != 2 {
		t.Errorf("sent %v, second seq %d", ids(got), got[1].cmd.GetSeq())
	}
}

// A send the stream cannot take leaves the row pending and unrecorded. The
// next epoch's Ready re-sends every pending command, the unacknowledged
// one it sent before included, with the same command_id and that epoch's
// seqs from 1; an acknowledged command is never sent again.
func TestANewEpochResendsWhatWasNotAcknowledged(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	h := newService(t, s, streams, fixedClock(t0))
	for _, id := range []string{"c1", "c2"} {
		mustEnqueue(t, s, "m1", id)
	}
	h.svc.Ready("m1", 1)
	streams.waitSent(t, 2)
	waitMarked(t, s, "c2", 1, 2)
	if _, err := h.svc.Ack(context.Background(), outbox.From{MachineID: "m1", Epoch: 1, Live: true}, ackOf("c1")); err != nil {
		t.Fatal(err)
	}
	streams.setLive("m1", 2) // the epoch 1 stream died
	h.svc.Notify(mustEnqueue(t, s, "m1", "c3"))
	waitSends(t, h, "not_live", 1)
	if r, _ := readRow(t, s, "c3"); r.state != "pending" || r.sentEpoch.Valid {
		t.Fatalf("c3 after a failed send: %+v", r)
	}
	h.svc.Gone("m1", 1)
	h.svc.Ready("m1", 2)
	got := streams.waitSent(t, 4)[2:]
	if !sameIDs(ids(got), "c2", "c3") {
		t.Fatalf("re-sent %v, want c2 c3", ids(got))
	}
	for i, g := range got {
		if g.epoch != 2 || g.cmd.GetEpoch() != 2 || g.cmd.GetSeq() != uint64(i+1) {
			t.Errorf("re-send %d: epoch %d seq %d, want 2 %d", i, g.cmd.GetEpoch(), g.cmd.GetSeq(), i+1)
		}
	}
	waitMarked(t, s, "c3", 2, 2)
}

// The same command_id enqueued twice is sent once.
func TestADuplicateEnqueueIsSentOnce(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	h := newService(t, s, streams, fixedClock(t0))
	h.svc.Notify(mustEnqueue(t, s, "m1", "c1"), mustEnqueue(t, s, "m1", "c1"))
	h.svc.Ready("m1", 1)
	streams.waitSent(t, 1)
	waitMarked(t, s, "c1", 1, 1)
	h.svc.Notify(mustEnqueue(t, s, "m1", "c2"))
	if got := ids(streams.waitSent(t, 2)); !sameIDs(got, "c1", "c2") {
		t.Errorf("sent %v, want c1 c2", got)
	}
	if lines := h.events(t, "outbox.enqueued"); len(lines) != 2 {
		t.Errorf("outbox.enqueued lines %v, want 2 (c1 once, c2)", lines)
	}
}

// A full stream queue sends nothing, uses no seq and is tried again.
func TestAFullQueueIsRetried(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	streams.setFull(true)
	h := newService(t, s, streams, fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	h.svc.Ready("m1", 1)
	waitSends(t, h, "full", 1)
	streams.setFull(false)
	got := streams.waitSent(t, 1)
	if got[0].cmd.GetSeq() != 1 {
		t.Errorf("seq %d after a full queue, want 1", got[0].cmd.GetSeq())
	}
}

// A command past its deadline (hub time) is not sent; expiry is the hub's.
// An older epoch's Ready does not replace a newer one.
func TestExpiredCommandsAndStaleReadiesAreNotSent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 3)
	h := newService(t, s, streams, fixedClock(t0.Add(time.Hour)))
	mustEnqueue(t, s, "m1", "old")
	fresh := interrupt("fresh")
	fresh.NotAfterTime.Seconds += 3600
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: fresh, Now: t0}); err != nil {
		t.Fatal(err)
	}
	h.svc.Ready("m1", 3)
	h.svc.Ready("m1", 2)
	if got := ids(streams.waitSent(t, 1)); !sameIDs(got, "fresh") {
		t.Errorf("sent %v, want only fresh", got)
	}
	waitMarked(t, s, "fresh", 3, 1)
}

// The outbox gauges hold each machine's pending count and oldest age.
func TestTheOutboxGaugesShowPendingCommands(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0.Add(90*time.Second)))
	mustEnqueue(t, s, "m1", "c1")
	mustEnqueue(t, s, "m1", "c2")
	mustEnqueue(t, s, "m2", "c3")
	if _, err := h.svc.Ack(context.Background(), live1, ackOf("c1")); err != nil {
		t.Fatal(err)
	}
	body := h.scrape(t)
	if v := sample(body, "fleet_hub_outbox_pending", `machine="m1"`); v != 1 {
		t.Errorf("pending{m1} = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_outbox_pending", `machine="m2"`); v != 1 {
		t.Errorf("pending{m2} = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_outbox_oldest_age_seconds", `machine="m1"`); v != 90 {
		t.Errorf("oldest age{m1} = %v, want 90", v)
	}
}

func waitSends(t *testing.T, h *harness, result string, n float64) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for sample(h.scrape(t), "fleet_hub_outbox_sends_total", `result="`+result+`"`) < n {
		if time.Now().After(end) {
			t.Fatalf("no %s send counted", result)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// When the job recording a send fails, the command is still not sent
// again on that epoch and its seq is not issued again: the next command
// gets seq 2. The failure is logged.
func TestASendWhoseRecordFailedIsNotRepeated(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `CREATE TRIGGER refuse_marks BEFORE UPDATE OF sent_epoch ON commands
			BEGIN SELECT RAISE(ABORT, 'test: marks refused'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	h := newService(t, s, streams, fixedClock(t0))
	h.svc.Notify(mustEnqueue(t, s, "m1", "c1"))
	h.svc.Ready("m1", 1)
	streams.waitSent(t, 1)
	waitEvent(t, h, "outbox.failed")
	h.svc.Notify(mustEnqueue(t, s, "m1", "c2"))
	got := streams.waitSent(t, 2)
	if !sameIDs(ids(got), "c1", "c2") || got[1].cmd.GetSeq() != 2 {
		t.Errorf("sent %v with second seq %d, want c1 c2 and seq 2", ids(got), got[1].cmd.GetSeq())
	}
	if lines := h.events(t, "outbox.failed"); len(lines) == 0 || lines[0]["step"] != "mark" {
		t.Errorf("outbox.failed lines %v", lines)
	}
}

func waitEvent(t *testing.T, h *harness, name string) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for len(h.events(t, name)) == 0 {
		if time.Now().After(end) {
			t.Fatalf("no %s line", name)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A stored command that no longer decodes is logged and skipped; the
// commands behind it still go out, and it uses no seq.
func TestAnUndecodableCommandDoesNotHoldUpTheRest(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	streams := newFakeStreams()
	streams.setLive("m1", 1)
	h := newService(t, s, streams, fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	mustEnqueue(t, s, "m1", "c2")
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE commands SET command = x'ff' WHERE command_id = 'c1'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.svc.Ready("m1", 1)
	got := streams.waitSent(t, 1)
	if !sameIDs(ids(got), "c2") || got[0].cmd.GetSeq() != 1 {
		t.Errorf("sent %v seq %d, want c2 with seq 1", ids(got), got[0].cmd.GetSeq())
	}
	waitMarked(t, s, "c2", 1, 1)
	if lines := h.events(t, "outbox.failed"); len(lines) != 1 || lines[0]["step"] != "decode" || lines[0]["command_id"] != "c1" {
		t.Errorf("outbox.failed lines %v", lines)
	}
	body := h.scrape(t)
	if v := sample(body, "fleet_hub_outbox_sends_total", `result="invalid"`); v != 1 {
		t.Errorf("sends{invalid} = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_outbox_sends_total", `result="not_live"`); v != -1 {
		t.Errorf("sends{not_live} = %v, want none", v)
	}
}
