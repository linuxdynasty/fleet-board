package conn_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
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
// read (still queued at the unacked_done step, right after the read), so
// the progress never follows the result the snapshot re-sends.
func TestWorkQueuedBeforeTheUnackedReadIsFlushedFirst(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	var armed, checkDone atomic.Bool
	var errs hookLog
	var loop *conn.Loop
	r := startLoopThen(t, a, func(o *conn.LoopOptions) {
		conn.SetStepHook(o, func(step string) {
			if step == "unacked_done" && checkDone.CompareAndSwap(true, false) {
				// The read is done and the flush has not run yet: what was
				// queued before the read is still in the queue.
				errs.add("queued at the read's end: %d", conn.QueueLen(loop))
				return
			}
			if step != "unacked_read" || !armed.CompareAndSwap(true, false) {
				return
			}
			checkDone.Store(true)
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
	if e := errs.get(); !slices.Equal(e, []string{"queued at the read's end: 2"}) {
		t.Fatalf("the steps saw %q, want the progress and the result still queued after the read", e)
	}
	if ids := h.snapshots(1)[1].GetUnackedResults(); len(ids) != 1 || ids[0].GetValue() != "x" {
		t.Fatalf("the requested snapshot lists %v, want x (logged before its read)", ids)
	}
	q := h.queued(1)
	if p, res := slices.Index(q, "progress:x"), slices.Index(q, "result:x"); p < 0 || p > res {
		t.Errorf("the hub got %q: the progress must come before the first result for x", q)
	}
}

// A Bye{REVOKED} waiting for the sender wins over a wake or a stop that is
// ready at the same moment: the loop ends as revoked and records it in
// ended.json. The sender is held by a hook while the Bye is read ahead and
// the wake or stop arrives, so both are ready when it returns; eight runs
// make a coin flip near certain to show.
//
// The receiver says "received" only after it handed a message over, so the
// Command's step can run after the hook took the Command. The test waits for
// the Bye's own step (the third message: HelloAck, Command, Bye; this fake
// sends nothing unasked), and holds the Command's step until the hook has
// the Command, so that late order runs every time.
func TestAWaitingByeWinsOverAWakeOrAStop(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"wake", "stop"} {
		for i := range 8 {
			t.Run(fmt.Sprintf("%s-%d", how, i), func(t *testing.T) {
				t.Parallel()
				h := startConnectHub(t)
				a := enrolled(t, h)
				events := make(chan wakenet.Event, 1)
				held, release, received := make(chan struct{}, 1), make(chan struct{}), make(chan struct{}, 8)
				late := make(chan struct{})
				var first atomic.Bool
				var n atomic.Int32
				r := startLoop(t, a, func(o *conn.LoopOptions) {
					o.Events = events
					o.Command = func(context.Context, uint64, *agentv1.Command) bool {
						if first.CompareAndSwap(false, true) {
							held <- struct{}{}
							<-release
						}
						return true
					}
					conn.SetStepHook(o, func(step string) {
						if step != "received" {
							return
						}
						switch n.Add(1) {
						case 2: // the Command's step, held until the hook has it
							<-late
						case 3: // the Bye's step: it is in the read-ahead
							received <- struct{}{}
						}
					})
				})
				h.connected(t, 1)
				r.connected(t)
				h.pushCommand(t, "hold", time.Now().Add(time.Hour))
				<-held
				close(late)
				h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: bye(agentv1.ByeReason_BYE_REASON_REVOKED)}})
				<-received // the Bye waits in the read-ahead
				if how == "wake" {
					events <- wake
				} else {
					r.cancel()
				}
				close(release)
				var ended *conn.EndedError
				if err := r.result(t, 10*time.Second); !errors.As(err, &ended) || ended.End != conn.EndRevoked {
					t.Fatalf("Run = %v, want the loop ended as revoked", err)
				}
				if _, err := os.Stat(filepath.Join(a.dir, conn.EndedFile)); err != nil {
					t.Errorf("ended.json: %v, want it written", err)
				}
			})
		}
	}
}

// A Snapshot hook error that a waiting Bye stands in for is still logged
// before the Bye decides (here a Bye{HUB_RESTART}: reconnect). As above,
// the test waits for the Bye's own "received" step (the second message,
// after HelloAck), with the HelloAck's step held until the hook runs.
func TestAFailureAByeStandsInForIsStillLogged(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	building, received, late := make(chan struct{}, 1), make(chan struct{}, 8), make(chan struct{})
	var first atomic.Bool
	var n atomic.Int32
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(context.Context, *agentv1.Snapshot) error {
			if !first.CompareAndSwap(false, true) {
				return nil
			}
			building <- struct{}{}
			<-received
			return errors.New("the hook failed")
		}
		conn.SetStepHook(o, func(step string) {
			if step != "received" {
				return
			}
			switch n.Add(1) {
			case 1: // the HelloAck's step, held until the hook runs
				<-late
			case 2: // the Bye's step: it is in the read-ahead
				received <- struct{}{}
			}
		})
	})
	<-building
	close(late)
	h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: bye(agentv1.ByeReason_BYE_REASON_HUB_RESTART)}})
	waitFor(t, 10*time.Second, "the next stream", func() bool { return h.count() >= 2 })
	var found bool
	for _, e := range a.events(t, "connect.failed") {
		found = found || e["step"] == "snapshot"
	}
	if !found {
		t.Errorf("connect.failed = %v, want the Snapshot hook's failure at step snapshot", a.events(t, "connect.failed"))
	}
	if b := a.events(t, "connect.bye"); len(b) == 0 || b[0]["reason"] != "HUB_RESTART" {
		t.Errorf("connect.bye = %v, want the hub's restart Bye acted on", b)
	}
}
