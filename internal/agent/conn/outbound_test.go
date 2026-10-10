package conn_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Send is refused at once while no stream is Connected.
func TestSendWithoutAConnectedStreamIsRefusedAtOnce(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	l, err := conn.NewLoop(context.Background(), conn.LoopOptions{
		Dir: a.dir, Identity: id, DB: a.openDB(t), Version: "test", BootID: "b", Telemetry: a.tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Send(ctx, ackOf("c1")); !errors.Is(err, conn.ErrNoStream) {
		t.Errorf("Send before Run = %v, want ErrNoStream", err)
	}
	if ctx.Err() != nil {
		t.Error("Send waited instead of refusing at once")
	}
}

// Only acks, progress, results and SettingsAck go through the queue, and a
// result only once it is logged, unchanged, in agent.db (durability before
// send, ruling fork 12 b).
func TestSendTakesOnlyQueueableKindsAndLoggedResults(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	ctx := context.Background()
	for _, m := range []*agentv1.ConnectRequest{
		nil,
		{},
		{Body: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{}}},
		{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: &agentv1.Snapshot{}}},
		{Body: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{}}},
		{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{}}},
		{Body: &agentv1.ConnectRequest_Bye{Bye: &agentv1.Bye{}}},
		{Body: &agentv1.ConnectRequest_CommandResult{}},
	} {
		if err := r.loop.Send(ctx, m); !errors.Is(err, conn.ErrNotQueueable) {
			t.Errorf("Send(%T) = %v, want ErrNotQueueable", m.GetBody(), err)
		}
	}
	if err := r.loop.Send(ctx, resultOf("never", typev1.Status_STATUS_OK)); !errors.Is(err, conn.ErrResultNotLogged) {
		t.Errorf("an unlogged result: %v, want ErrResultNotLogged", err)
	}
	if _, _, err := db.LogCommand(ctx, ring.Command{
		ID: "open", Kind: "capture_pane", Epoch: 1, Seq: 1, Received: time.Now(), NotAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.loop.Send(ctx, resultOf("open", typev1.Status_STATUS_OK)); !errors.Is(err, conn.ErrResultNotLogged) {
		t.Errorf("a result for a command with none logged: %v, want ErrResultNotLogged", err)
	}
	logResult(t, db, "done", time.Now())
	if err := r.loop.Send(ctx, resultOf("done", typev1.Status_STATUS_FAILED)); !errors.Is(err, conn.ErrResultNotLogged) {
		t.Errorf("a result unlike the logged one: %v, want ErrResultNotLogged", err)
	}
	if err := r.loop.Send(ctx, resultOf("done", typev1.Status_STATUS_OK)); err != nil {
		t.Fatalf("the logged result: %v", err)
	}
	// The two refusals that are never logged (02-contracts "Envelope
	// checks", ruling fork 4 i) go out for an id the log does not hold;
	// no other unlogged result does, and a logged id gets only its result.
	const (
		failed   = typev1.Status_STATUS_FAILED
		rejected = typev1.Status_STATUS_REJECTED_BY_POLICY
		epochMM  = typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH
		replayed = typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED
	)
	for _, c := range []struct {
		id     string
		st     typev1.Status
		why    typev1.ReasonCode
		wantOK bool
	}{
		{"em", failed, epochMM, true},
		{"sr", rejected, replayed, true},
		{"x1", failed, replayed, false},
		{"x2", rejected, epochMM, false},
		{"x3", failed, typev1.ReasonCode_REASON_CODE_PANE_BUSY, false},
		{"done", failed, epochMM, false},
	} {
		err := r.loop.Send(ctx, refusalOf(c.id, c.st, c.why))
		if c.wantOK && err != nil || !c.wantOK && !errors.Is(err, conn.ErrResultNotLogged) {
			t.Errorf("Send(%s %v/%v) = %v, want ok %v", c.id, c.st, c.why, err, c.wantOK)
		}
	}
	// A logged result that cannot be read is refused and logged.
	if _, _, err := db.LogCommand(ctx, ring.Command{
		ID: "garbled", Kind: "capture_pane", Epoch: 1, Seq: 2, Received: time.Now(), NotAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordResult(ctx, "garbled", []byte{0xff, 0xff, 0xff}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.loop.Send(ctx, resultOf("garbled", typev1.Status_STATUS_OK)); !errors.Is(err, conn.ErrResultNotLogged) {
		t.Errorf("a result whose logged copy cannot be read: %v, want ErrResultNotLogged", err)
	}
	if ev := a.events(t, "command.result_unreadable"); len(ev) != 1 || ev[0]["command_id"] != "garbled" ||
		ev[0]["err_kind"] != "parse" {
		t.Errorf("command.result_unreadable = %v, want one line for garbled", ev)
	}
	progress := &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{
		CommandProgress: &agentv1.CommandProgress{CommandId: &typev1.CommandId{Value: "open"}},
	}}
	settingsAck := &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_SettingsAck{SettingsAck: &agentv1.SettingsAck{Generation: 2}}}
	for _, m := range []*agentv1.ConnectRequest{progress, settingsAck} {
		if err := r.loop.Send(ctx, m); err != nil {
			t.Errorf("Send(%T) = %v", m.GetBody(), err)
		}
	}
	waitFor(t, 5*time.Second, "the five messages", func() bool { return len(h.stream(1).msgs) >= 6 })
	got := h.stream(1).msgs[1:]
	if got[0].GetCommandResult().GetCommandId().GetValue() != "done" ||
		got[1].GetCommandResult().GetCommandId().GetValue() != "em" ||
		got[2].GetCommandResult().GetCommandId().GetValue() != "sr" || got[3].GetCommandProgress() == nil ||
		got[4].GetSettingsAck().GetGeneration() != 2 || len(got) != 5 {
		t.Errorf("after the snapshot the hub got %v", got)
	}
}

// Messages from many goroutines are sent by the stream's one sender, each
// goroutine's in its order, and never between a snapshot and its results,
// whatever SnapshotRequests come meanwhile (run under -race).
func TestQueuedMessagesNeverLandBetweenASnapshotAndItsResults(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	logResult(t, db, "u1", time.Now())
	logResult(t, db, "u2", time.Now())
	h.set(heartbeatEvery(durationpb.New(time.Second)))
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	const senders, each, requests = 8, 40, 15
	var wg sync.WaitGroup
	errs := make(chan error, senders*each)
	for g := range senders {
		wg.Go(func() {
			for k := range each {
				if err := r.loop.Send(context.Background(), ackOf(fmt.Sprintf("g%d-%03d", g, k))); err != nil {
					errs <- err
				}
			}
		})
	}
	for range requests {
		h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("Send: %v", err)
	}
	waitFor(t, 10*time.Second, "every ack and snapshot", func() bool {
		return len(h.acks(1)) == senders*each && len(h.snapshots(1)) == requests+1
	})
	q := h.queued(1)
	for i, m := range q {
		if m == "snapshot" && (i+2 >= len(q) || q[i+1] != "result:u1" || q[i+2] != "result:u2") {
			t.Fatalf("message %d is a snapshot not followed by its results: %q", i, q[i:min(i+4, len(q))])
		}
	}
	last := map[string]int{}
	for _, id := range h.acks(1) {
		g, k, _ := strings.Cut(id, "-")
		n, _ := strconv.Atoi(k)
		if prev, ok := last[g]; ok && n != prev+1 {
			t.Fatalf("sender %s: ack %d came after %d", g, n, prev)
		}
		last[g] = n
	}
}

// A full queue makes Send wait for room, never past its context; the
// queued messages still go out in order once the sender is free again.
func TestSendWaitsForRoomButNotPastItsContext(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	held, g := make(chan struct{}, 1), newGate()
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		// The hook runs on the sending goroutine, so holding it holds the
		// queue's only reader.
		o.Command = func(context.Context, uint64, *agentv1.Command) bool {
			held <- struct{}{}
			_ = g.wait(context.Background())
			return true
		}
	})
	t.Cleanup(g.open)
	h.connected(t, 1)
	r.connected(t)
	h.pushCommand(t, "hold", time.Now().Add(time.Hour))
	<-held
	ctx := context.Background()
	for k := range conn.QueueSize {
		if err := r.loop.Send(ctx, ackOf(strconv.Itoa(k))); err != nil {
			t.Fatalf("Send %d into a queue with room: %v", k, err)
		}
	}
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if err := r.loop.Send(short, ackOf("over")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send into a full queue = %v, want its context's end", err)
	}
	again, cancel2 := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel2()
	if err := r.loop.Send(again, ackOf("over")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second Send into a full queue = %v, want its context's end", err)
	}
	if ev := a.events(t, "connect.outbound_blocked"); len(ev) != 1 || ev[0]["kind"] != "command_ack" {
		t.Errorf("connect.outbound_blocked = %v, want one line per stream with kind command_ack", ev)
	}
	if got := metric(scrape(t, a), "fleet_agent_outbound_blocked_total", `kind="command_ack"`); got != "2" {
		t.Errorf("fleet_agent_outbound_blocked_total = %q, want 2: every wait counts", got)
	}
	g.open()
	waitFor(t, 10*time.Second, "the queued acks", func() bool { return len(h.acks(1)) == conn.QueueSize })
	for k, id := range h.acks(1) {
		if id != strconv.Itoa(k) {
			t.Fatalf("ack %d is %q; the queue keeps its order", k, id)
		}
	}
}

// The queue belongs to its stream: what is left in it when the stream ends
// is never sent on the next one, and a Send waiting on it is refused.
func TestTheQueueDiesWithItsStream(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	held, g := make(chan struct{}, 1), newGate()
	first := true
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Command = func(context.Context, uint64, *agentv1.Command) bool {
			if first {
				first = false
				held <- struct{}{}
				_ = g.wait(context.Background())
			}
			return true
		}
	})
	t.Cleanup(g.open)
	h.connected(t, 1)
	r.connected(t)
	h.pushCommand(t, "hold", time.Now().Add(time.Hour))
	<-held
	ctx := context.Background()
	for k := range conn.QueueSize {
		if err := r.loop.Send(ctx, ackOf("old-"+strconv.Itoa(k))); err != nil {
			t.Fatal(err)
		}
	}
	waiting := make(chan error, 1)
	go func() { waiting <- r.loop.Send(ctx, ackOf("waiting")) }()
	h.dropLive(t)
	g.open()
	select {
	case err := <-waiting:
		// The sender may free one slot before it sees the stream gone, so
		// the wait can end queued (nil) on the dying queue; it must not
		// hang or fail otherwise. queue_internal_test.go pins ErrNoStream.
		if err != nil && !errors.Is(err, conn.ErrNoStream) {
			t.Errorf("a Send waiting on a dead stream's queue = %v, want ErrNoStream or queued", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a Send waiting on a dead stream's queue never returned")
	}
	h.connected(t, 2)
	r.connected(t)
	if err := r.loop.Send(ctx, ackOf("new")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the new ack", func() bool { return len(h.acks(2)) > 0 })
	if got := h.acks(2); !slices.Equal(got, []string{"new"}) {
		t.Errorf("stream 2 got acks %q; the old stream's queue must die with it", got)
	}
	// What died in the queue is counted, as a lower bound: the sender may
	// take a few before it sees the stream gone, and the waiting Send may
	// add one.
	lost, _ := strconv.Atoi(metric(scrape(t, a), "fleet_agent_outbound_lost_total"))
	if lost < conn.QueueSize/2 || lost > conn.QueueSize+1 {
		t.Errorf("fleet_agent_outbound_lost_total = %d, want about %d", lost, conn.QueueSize)
	}
}
