package conn_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A Bye{REVOKED} or Bye{UNKNOWN_MACHINE} the hub sends just before it ends
// the stream still ends the loop when a hook held the sender meanwhile:
// the receiver read the Bye ahead and then saw the end, and the sender
// must act on the Bye, not on the end (03 W1 "The end").
func TestAByeBeforeTheStreamEndsIsNotLostToAHeldSender(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		reason agentv1.ByeReason
		want   conn.End
	}{
		{agentv1.ByeReason_BYE_REASON_REVOKED, conn.EndRevoked},
		{agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE, conn.EndUnknownMachine},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			held := make(chan struct{}, 1)
			var first atomic.Bool
			r := startLoop(t, a, func(o *conn.LoopOptions) {
				// The hook holds the sender until its context ends, which
				// is once the receiver has read the Bye and the end after it.
				o.Command = func(ctx context.Context, _ uint64, _ *agentv1.Command) bool {
					if first.CompareAndSwap(false, true) {
						held <- struct{}{}
						<-ctx.Done()
					}
					return true
				}
			})
			h.connected(t, 1)
			r.connected(t)
			h.pushCommand(t, "hold", time.Now().Add(time.Hour))
			<-held
			// A queued message too, so the sender may also meet the end in a
			// send, which drains what the hub sent before it.
			if err := r.loop.Send(context.Background(), ackOf("queued")); err != nil {
				t.Fatal(err)
			}
			h.sendThenEnd(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: bye(tc.reason)}})
			var ended *conn.EndedError
			if err := r.result(t, 10*time.Second); !errors.As(err, &ended) || ended.End != tc.want {
				t.Fatalf("Run = %v, want the loop ended as %s", err, tc.want)
			}
			if h.count() != 1 {
				t.Errorf("the agent opened %d streams; a revoked agent must not reconnect", h.count())
			}
		})
	}
}

// A progress, its logged result and the result, queued by another
// goroutine just before a requested snapshot reads the unacked results,
// reach the hub in that order: the snapshot flushes the queue after its
// read, so the progress never follows the result the snapshot re-sends.
func TestWorkQueuedBeforeTheUnackedReadIsFlushedFirst(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	var armed atomic.Bool
	var errs hookLog
	var loop *conn.Loop
	r := startLoopThen(t, a, func(o *conn.LoopOptions) {
		conn.SetStepHook(o, func(step string) {
			if step != "unacked_read" || !armed.CompareAndSwap(true, false) {
				return
			}
			done := make(chan struct{})
			go func() { // a command goroutine, not the sender
				defer close(done)
				ctx := context.Background()
				if err := loop.Send(ctx, progressOf("x")); err != nil {
					errs.add("progress: %v", err)
				}
				if err := logFinished(ctx, db, "x"); err != nil {
					errs.add("log: %v", err)
				}
				if err := loop.Send(ctx, resultOf("x", typev1.Status_STATUS_OK)); err != nil {
					errs.add("result: %v", err)
				}
			}()
			<-done
		})
	}, func(l *conn.Loop) { loop = l })
	h.connected(t, 1)
	r.connected(t)
	armed.Store(true)
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	waitFor(t, 10*time.Second, "the second snapshot and its result", func() bool {
		q := h.queued(1)
		at := snapshotsAt(q)
		return len(at) == 2 && len(q) > at[1]+1
	})
	if e := errs.get(); len(e) != 0 {
		t.Fatalf("the command goroutine failed: %q", e)
	}
	if ids := h.snapshots(1)[1].GetUnackedResults(); len(ids) != 1 || ids[0].GetValue() != "x" {
		t.Fatalf("the requested snapshot lists %v, want x (logged before its read)", ids)
	}
	q := h.queued(1)
	if p, res := slices.Index(q, "progress:x"), slices.Index(q, "result:x"); p < 0 || p > res {
		t.Errorf("the hub got %q: the progress must come before the first result for x", q)
	}
}
