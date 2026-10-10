package conn_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestASnapshotCutShortIsNeverSentOnTheNextStream(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// honorCtx: the build stops when its context ends; otherwise it
		// finishes after the stream is gone.
		honorCtx bool
	}{
		{"the build sees the stream end", true},
		{"the build ignores the stream end", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			g := newGate()
			building := make(chan struct{})
			var mu sync.Mutex
			var built []string
			startLoop(t, a, func(o *conn.LoopOptions) {
				o.Snapshot = func(ctx context.Context, s *agentv1.Snapshot) error {
					mu.Lock()
					built = append(built, s.GetSnapshotId())
					first := len(built) == 1
					mu.Unlock()
					if !first {
						return nil
					}
					close(building)
					if tc.honorCtx {
						return g.wait(ctx)
					}
					return g.wait(context.Background())
				}
			})
			select {
			case <-building:
			case <-time.After(10 * time.Second):
				t.Fatal("no snapshot was built")
			}
			h.dropLive(t)
			waitFor(t, 5*time.Second, "stream 1 to end", func() bool { return h.stream(1).ended })
			g.open()
			h.connected(t, 2)

			mu.Lock()
			cut := built[0]
			mu.Unlock()
			if got := h.snapshots(1); len(got) != 0 {
				t.Errorf("the dropped stream got %d snapshots", len(got))
			}
			for n := 2; n <= h.count(); n++ {
				for _, s := range h.snapshots(n) {
					if s.GetSnapshotId() == cut {
						t.Errorf("stream %d got the snapshot built for stream 1", n)
					}
				}
			}
		})
	}
}

// logResult logs a finished command received at received, with its result.
func logResult(t *testing.T, db *ring.DB, id string, received time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := db.LogCommand(ctx, ring.Command{
		ID: id, Kind: "capture_pane", Epoch: 1, Seq: 1, Received: received, NotAfter: received.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := proto.Marshal(&agentv1.CommandResult{
		CommandId: &typev1.CommandId{Value: id}, Status: typev1.Status_STATUS_OK,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordResult(ctx, id, res, received.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

// delivered lists stream n's messages after Hello as "snapshot[ids]" and
// "result:id".
func delivered(h *connectHub, n int) []string {
	var out []string
	for _, m := range h.stream(n).msgs {
		switch {
		case m.GetSnapshot() != nil:
			ids := "snapshot["
			for i, id := range m.GetSnapshot().GetUnackedResults() {
				if i > 0 {
					ids += " "
				}
				ids += id.GetValue()
			}
			out = append(out, ids+"]")
		case m.GetCommandResult() != nil:
			out = append(out, "result:"+m.GetCommandResult().GetCommandId().GetValue())
		}
	}
	return out
}

func TestResultsAreResentAfterEachSnapshotUntilAcked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	now := time.Now()
	logResult(t, db, "c-old", now.Add(-8*24*time.Hour))
	logResult(t, db, "c1", now.Add(-2*time.Minute))
	logResult(t, db, "c2", now.Add(-time.Minute))
	if _, _, err := db.LogCommand(ctx, ring.Command{
		ID: "c3", Kind: "capture_pane", Epoch: 1, Seq: 2, Received: now, NotAfter: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	r := startLoop(t, a, nil)
	until := func(n, count int) {
		t.Helper()
		waitFor(t, 10*time.Second, "messages on the stream", func() bool {
			return h.count() >= n && len(delivered(h, n)) >= count
		})
	}
	until(1, 3)
	if got, want := delivered(h, 1), []string{"snapshot[c1 c2]", "result:c1", "result:c2"}; !slices.Equal(got, want) {
		t.Fatalf("stream 1 got %v, want %v", got, want)
	}
	if _, err := db.Command(ctx, "c-old"); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("an entry older than 7 days is still logged: %v", err)
	}

	h.push(t, resultAck("c1", "never-logged", "c3", "c-old"))
	waitFor(t, 5*time.Second, "c1 acked", func() bool {
		e, err := db.Command(ctx, "c1")
		return err == nil && !e.Acked.IsZero()
	})
	if e, err := db.Command(ctx, "c3"); err != nil || !e.Acked.IsZero() {
		t.Errorf("an ack for a running command changed it: %+v, %v", e, err)
	}
	for _, id := range []string{"never-logged", "c-old"} {
		if _, err := db.Command(ctx, id); !errors.Is(err, ring.ErrUnknownCommand) {
			t.Errorf("an ack for the unknown or pruned %s logged it: %v", id, err)
		}
	}
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{
		SnapshotRequest: &agentv1.SnapshotRequest{Reason: "event_lost"},
	}})
	until(1, 5)
	if got, want := delivered(h, 1)[3:], []string{"snapshot[c2]", "result:c2"}; !slices.Equal(got, want) {
		t.Errorf("after ResultAck{c1} and a SnapshotRequest stream 1 got %v, want %v", got, want)
	}
	if h.stream(1).ended {
		t.Error("an ack for unknown ids ended the stream")
	}

	h.dropLive(t)
	until(2, 2)
	if got, want := delivered(h, 2), []string{"snapshot[c2]", "result:c2"}; !slices.Equal(got, want) {
		t.Errorf("stream 2 got %v, want %v", got, want)
	}
	h.push(t, resultAck("c2"))
	waitFor(t, 5*time.Second, "c2 acked", func() bool {
		e, err := db.Command(ctx, "c2")
		return err == nil && !e.Acked.IsZero()
	})
	h.dropLive(t)
	until(3, 1)
	time.Sleep(50 * time.Millisecond)
	if got, want := delivered(h, 3), []string{"snapshot[]"}; !slices.Equal(got, want) {
		t.Errorf("after every ack stream 3 got %v, want %v", got, want)
	}
	r.running(t)
}

// A Snapshot supersedes every event with a seq at or below its own (02i),
// so its seq is taken before it is built: an event emitted while it is
// built gets a higher seq and is not superseded by state read before it.
func TestTheSnapshotSeqIsTakenBeforeItIsBuilt(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	var mu sync.Mutex
	var during uint64
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(_ context.Context, s *agentv1.Snapshot) error {
			mu.Lock()
			defer mu.Unlock()
			during = s.GetSeq()
			s.Seq = 0 // the hook cannot change it
			return nil
		}
	})
	h.connected(t, 1)
	sent := h.snapshots(1)[0].GetSeq()
	mu.Lock()
	defer mu.Unlock()
	if during == 0 || during != sent {
		t.Errorf("the build saw seq %d, the hub got %d; want the same seq, taken before the build", during, sent)
	}
}

func TestTheEventSeqNeverGoesBackwards(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	// Stream 2's hub holds seqs the agent never handed out (a wiped
	// agent.db); stream 3's holds fewer (a hub restored from a backup).
	h.set(func(h *connectHub) {
		h.tamperAck = func(n int, ack *agentv1.HelloAck) {
			switch n {
			case 2:
				ack.HubLastEventSeq = 50_000
			case 3:
				ack.HubLastEventSeq = 5
			}
		}
	})
	startLoop(t, a, nil)
	var seqs []uint64
	for n := 1; n <= 3; n++ {
		h.connected(t, n)
		seqs = append(seqs, h.snapshots(n)[0].GetSeq())
		if n < 3 {
			h.dropLive(t)
		}
	}
	if seqs[1] <= 50_000 || seqs[2] <= seqs[1] || seqs[1] <= seqs[0] {
		t.Errorf("snapshot seqs %v: each must be above the last and above the hub's 50000", seqs)
	}
	for n := 2; n <= 3; n++ {
		prev, cur := h.stream(n-1).hello, h.stream(n).hello
		if cur.GetLastEventSeq() < prev.GetLastEventSeq() || cur.GetLastEventSeq() < seqs[n-2] {
			t.Errorf("Hello %d last_event_seq %d after %d and snapshot seq %d", n, cur.GetLastEventSeq(),
				prev.GetLastEventSeq(), seqs[n-2])
		}
		if want := uint64(n - 1); cur.GetLastEpoch() != want {
			t.Errorf("Hello %d last_epoch = %d, want %d (the epoch of the stream before)", n, cur.GetLastEpoch(), want)
		}
	}
}
