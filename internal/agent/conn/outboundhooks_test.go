package conn_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A hook on the sending goroutine that waits on Send (a misuse: the queue
// is full and only that goroutine drains it) is released when the stream
// or the run ends, so the agent never hangs and Run still stops.
func TestAHookWaitingOnSendIsReleasedWhenTheStreamOrRunEnds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, h *connectHub, r *loopRun)
		// within bounds the release; a stop must not wait for the 2 s cut
		// of a stream that does not close (shutdownWait).
		within time.Duration
	}{
		{"the stream ends", func(t *testing.T, h *connectHub, _ *loopRun) { t.Helper(); h.dropLive(t) }, 10 * time.Second},
		{"the run stops", func(_ *testing.T, _ *connectHub, r *loopRun) { r.cancel() }, 1500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			got := make(chan error, 1)
			var loop *conn.Loop
			first := true
			r := startLoopThen(t, a, func(o *conn.LoopOptions) {
				o.Command = func(context.Context, uint64, *agentv1.Command) bool {
					if !first {
						return true
					}
					first = false
					for k := range conn.QueueSize {
						if err := loop.Send(context.Background(), ackOf(strconv.Itoa(k))); err != nil {
							got <- err
							return true
						}
					}
					got <- loop.Send(context.Background(), ackOf("over"))
					return true
				}
			}, func(l *conn.Loop) { loop = l })
			h.connected(t, 1)
			r.connected(t)
			h.pushCommand(t, "c1", time.Now().Add(time.Hour))
			waitFor(t, 5*time.Second, "the hook to wait on Send", func() bool {
				return len(a.events(t, "connect.outbound_blocked")) == 1
			})
			select {
			case err := <-got:
				t.Fatalf("the hook's Send returned %v while the stream and run were live", err)
			default:
			}
			tc.end(t, h, r)
			select {
			case err := <-got:
				if !errors.Is(err, conn.ErrNoStream) {
					t.Errorf("the hook's Send = %v, want ErrNoStream", err)
				}
			case <-time.After(tc.within):
				t.Fatalf("the hook's Send was not released within %v", tc.within)
			}
			if tc.name == "the run stops" {
				if err := r.result(t, 10*time.Second); err != nil {
					t.Errorf("Run = %v, want nil after a stop", err)
				}
				return
			}
			h.connected(t, 2)
			r.connected(t)
		})
	}
}

// 03n: a SnapshotRequest first sends what the queue holds, so a progress
// queued before it never reaches the hub after the result the snapshot
// re-sends. Each Command's hook queues progress, logs and queues the
// result, then gives the SnapshotRequest pushed after it time to wait on
// the stream, so the loop has both ready; twenty rounds make an unlucky
// pass without the flush near impossible.
func TestProgressQueuedBeforeASnapshotRequestGoesBeforeIts(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	db := a.openDB(t)
	var errs hookLog
	var loop *conn.Loop
	r := startLoopThen(t, a, func(o *conn.LoopOptions) {
		o.Command = func(ctx context.Context, _ uint64, c *agentv1.Command) bool {
			id := c.GetCommandId().GetValue()
			if err := loop.Send(ctx, progressOf(id)); err != nil {
				errs.add("progress %s: %v", id, err)
			}
			if err := logFinished(ctx, db, id); err != nil {
				errs.add("log %s: %v", id, err)
			}
			if err := loop.Send(ctx, resultOf(id, typev1.Status_STATUS_OK)); err != nil {
				errs.add("result %s: %v", id, err)
			}
			time.Sleep(20 * time.Millisecond)
			return true
		}
	}, func(l *conn.Loop) { loop = l })
	h.connected(t, 1)
	r.connected(t)
	const rounds = 20
	for k := range rounds {
		h.pushCommand(t, fmt.Sprintf("c%02d", k), time.Now().Add(time.Hour))
		h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_SnapshotRequest{SnapshotRequest: &agentv1.SnapshotRequest{}}})
	}
	waitFor(t, 20*time.Second, "every round's snapshot", func() bool { return len(h.snapshots(1)) == rounds+1 })
	if e := errs.get(); len(e) != 0 {
		t.Fatalf("the hook failed: %q", e)
	}
	q := h.queued(1)
	for k := range rounds {
		id := fmt.Sprintf("c%02d", k)
		p, res := slices.Index(q, "progress:"+id), slices.Index(q, "result:"+id)
		if p < 0 || res < 0 || p > res {
			t.Fatalf("%s: progress at %d, first result at %d; the progress must come first: %q", id, p, res, q)
		}
	}
	for i, m := range q {
		if m != "snapshot" {
			continue
		}
		for j, id := range h.snapshots(1)[slices.Index(snapshotsAt(q), i)].GetUnackedResults() {
			if i+1+j >= len(q) || q[i+1+j] != "result:"+id.GetValue() {
				t.Fatalf("the snapshot at %d is not followed by its results: %q", i, q[i:])
			}
		}
	}
}

// snapshotsAt lists the indexes of the snapshots in q.
func snapshotsAt(q []string) []int {
	var out []int
	for i, m := range q {
		if m == "snapshot" {
			out = append(out, i)
		}
	}
	return out
}
