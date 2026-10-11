package conn_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// seededEmail is a secret the test executor puts in each result message;
// the hub must scrub it before it stores the result.
const seededEmail = "al@example.com"

// interruptCmd is a hub command for the outbox: an Interrupt with id and
// a deadline an hour away; the outbox sets epoch and seq when it sends.
func interruptCmd(id string) *agentv1.Command {
	return &agentv1.Command{
		CommandId:    &typev1.CommandId{Value: id},
		NotAfterTime: timestamppb.New(time.Now().Add(time.Hour)),
		Kind:         &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{Server: "default"}}},
	}
}

// testExecutor is the smallest executor behind LoopOptions.Command (ruling
// row 6, fork 4): the hook records what came and hands the work to a
// goroutine, which logs the command in agent.db, sends CommandAck, logs
// its OK result and sends it. A command listed in swallow for an epoch is
// taken and never answered on that epoch, as if the stream died before
// the ack. With hold set, each result waits for hold to open after the
// ack.
type testExecutor struct {
	loop    *conn.Loop
	db      *ring.DB
	swallow map[string]uint64
	hold    *gate

	mu   sync.Mutex
	seen []string
	errs []error
	wg   sync.WaitGroup
}

func (x *testExecutor) hook(_ context.Context, epoch uint64, c *agentv1.Command) bool {
	id := c.GetCommandId().GetValue()
	x.mu.Lock()
	x.seen = append(x.seen, fmt.Sprintf("%s epoch %d seq %d", id, c.GetEpoch(), c.GetSeq()))
	x.mu.Unlock()
	if c.GetEpoch() != epoch {
		x.fail(fmt.Errorf("%s came with epoch %d on stream epoch %d", id, c.GetEpoch(), epoch))
	}
	if x.swallow[id] == epoch {
		return true
	}
	x.wg.Go(func() { x.run(c) })
	return true
}

func (x *testExecutor) run(c *agentv1.Command) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := c.GetCommandId().GetValue()
	if _, _, err := x.db.LogCommand(ctx, ring.Command{
		ID: id, Kind: "interrupt", Epoch: c.GetEpoch(), Seq: c.GetSeq(), Received: time.Now(), NotAfter: c.GetNotAfterTime().AsTime(),
	}); err != nil {
		x.fail(err)
		return
	}
	if err := x.loop.Send(ctx, ackOf(id)); err != nil {
		x.fail(fmt.Errorf("ack %s: %w", id, err))
		return
	}
	if x.hold != nil {
		if err := x.hold.wait(ctx); err != nil {
			x.fail(fmt.Errorf("hold %s: %w", id, err))
			return
		}
	}
	res := &agentv1.CommandResult{
		CommandId: &typev1.CommandId{Value: id}, Status: typev1.Status_STATUS_OK,
		Message: "interrupted; told " + seededEmail,
	}
	body, err := proto.Marshal(res)
	if err == nil {
		err = x.db.RecordResult(ctx, id, body, time.Now())
	}
	if err == nil {
		err = x.loop.Send(ctx, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: res}})
	}
	if err != nil {
		x.fail(fmt.Errorf("result %s: %w", id, err))
	}
}

func (x *testExecutor) fail(err error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.errs = append(x.errs, err)
}

func (x *testExecutor) got() []string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return slices.Clone(x.seen)
}

// done waits until the hub closed id with its result and the agent took
// the ResultAck (agent.db's acked time).
func done(t *testing.T, h *conntest.Hub, a *agent, machine, id string) {
	t.Helper()
	waitFor(t, 10*time.Second, id+" done on the hub", func() bool {
		for _, c := range h.Commands(t, machine) {
			if c.CommandID == id {
				return c.State == "done"
			}
		}
		return false
	})
	waitFor(t, 10*time.Second, "the ResultAck of "+id+" in agent.db", func() bool {
		e, err := a.openDB(t).Command(context.Background(), id)
		return err == nil && !e.Acked.IsZero()
	})
}

// The command path end to end, through the real hub listener and the
// agent's real loop (plan row 5 PR 5f, 02i "Command lifecycle"): a command
// enqueued before the agent connects waits for the epoch's Snapshot to
// commit, and only the Snapshot's commit sends it (nothing calls Ready by
// hand); the agent's ack and result are stored, the result scrubbed, and
// the ResultAck reaches agent.db. A command enqueued once the machine is
// ready goes out at once with the epoch's next seq. After a hub restart
// (epoch 2) a finished command is never sent again, and one the agent
// never acked goes out again with the same command_id and seq 1 of the
// new epoch.
func TestTheRealHubSendsCommandsAfterTheSnapshotAndTakesTheirResults(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	ident, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	id := ident.MachineID
	h.Enqueue(t, id, interruptCmd("c-before"))

	x := &testExecutor{db: a.openDB(t), swallow: map[string]uint64{"c-unacked": 1}}
	snap := newGate()
	r := startLoopThen(t, a, func(o *conn.LoopOptions) {
		o.Command = x.hook
		o.Snapshot = func(ctx context.Context, _ *agentv1.Snapshot) error { return snap.wait(ctx) }
	}, func(l *conn.Loop) { x.loop = l })

	// Hello committed and the stream is live, but its Snapshot is held.
	waitFor(t, 10*time.Second, "a live epoch 1 row", func() bool {
		c := h.Connections(t, id)
		return len(c) == 1 && c[0].Epoch == 1 && c[0].EndReason == ""
	})
	time.Sleep(200 * time.Millisecond) // a window in which an early send would land
	if got := x.got(); len(got) != 0 {
		t.Fatalf("the agent got %q before its Snapshot", got)
	}
	if n := len(h.Events(t, "outbox.sent")); n != 0 {
		t.Fatalf("%d outbox.sent lines before the Snapshot", n)
	}
	snap.open()
	done(t, h, a, id, "c-before")
	sent := h.Events(t, "outbox.sent")
	applied := strings.Index(h.LogText(), `"msg":"snapshot.applied"`)
	first := strings.Index(h.LogText(), `"msg":"outbox.sent"`)
	if len(sent) != 1 || applied < 0 || first < applied {
		t.Fatalf("outbox.sent %v at %d, snapshot.applied at %d: want one send after the Snapshot", sent, first, applied)
	}

	// The machine is ready: two more go out at once, in enqueue order.
	h.Enqueue(t, id, interruptCmd("c-unacked"), interruptCmd("c-after"))
	done(t, h, a, id, "c-after")
	waitFor(t, 10*time.Second, "the send of c-unacked recorded", func() bool {
		return h.Commands(t, id)[1].SentEpoch == 1
	})
	want := []string{"c-before epoch 1 seq 1", "c-unacked epoch 1 seq 2", "c-after epoch 1 seq 3"}
	if got := x.got(); !slices.Equal(got, want) {
		t.Fatalf("epoch 1 deliveries %q, want %q", got, want)
	}

	h.Restart(t)
	waitFor(t, 10*time.Second, "epoch 2 on the hub", func() bool { return h.LastEpoch(t, id) == 2 })
	done(t, h, a, id, "c-unacked")
	x.wg.Wait()
	want = append(want, "c-unacked epoch 2 seq 1")
	if got := x.got(); !slices.Equal(got, want) {
		t.Errorf("deliveries %q, want %q: no finished command again, the unacked one on seq 1 of epoch 2", got, want)
	}
	wantRows := []conntest.CommandRow{
		{CommandID: "c-before", State: "done", OpState: "DONE", Status: "OK", SentEpoch: 1, SentSeq: 1, Acked: true},
		{CommandID: "c-unacked", State: "done", OpState: "DONE", Status: "OK", SentEpoch: 2, SentSeq: 1, Acked: true},
		{CommandID: "c-after", State: "done", OpState: "DONE", Status: "OK", SentEpoch: 1, SentSeq: 3, Acked: true},
	}
	if got := h.Commands(t, id); !slices.Equal(got, wantRows) {
		t.Errorf("commands rows\n%+v\nwant\n%+v", got, wantRows)
	}
	res := h.Results(t, id)
	epochs := map[string]uint64{}
	for _, rr := range res {
		epochs[rr.CommandID] = rr.Epoch
		if rr.Status != "OK" || strings.Contains(rr.Message, seededEmail) || !strings.HasPrefix(rr.Message, "interrupted; told ") {
			t.Errorf("stored result %+v: want OK with the email scrubbed out", rr)
		}
	}
	if len(res) != 3 || epochs["c-before"] != 1 || epochs["c-after"] != 1 || epochs["c-unacked"] != 2 {
		t.Errorf("command_results %+v, want one per command, c-unacked's from epoch 2", res)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if len(x.errs) != 0 {
		t.Errorf("the executor failed: %v", x.errs)
	}
	r.running(t)
}
