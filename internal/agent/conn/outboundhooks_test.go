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
		// The hub sends on its own (a ResultAck, Settings, a SnapshotRequest):
		// a message after the held one must not hide the stream's end.
		{"the stream ends after another message", func(t *testing.T, h *connectHub, _ *loopRun) {
			t.Helper()
			h.pushSettings(t, 9)
			time.Sleep(50 * time.Millisecond)
			h.dropLive(t)
		}, 10 * time.Second},
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
	waitFor(t, 20*time.Second, "every round's snapshot and its results", func() bool {
		snaps := h.snapshots(1)
		if len(snaps) != rounds+1 {
			return false
		}
		q := h.queued(1)
		at := snapshotsAt(q)
		return len(q)-at[len(at)-1]-1 >= len(snaps[rounds].GetUnackedResults())
	})
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

// Connected means Send takes messages (ruling fork 12 a): right after the
// status says Connected a Send is queued, and a Send refused because the
// stream's queue closed never sees Connected. The step hook runs on Run's
// goroutine at both moments, so the check is exact, not a race.
func TestConnectedMeansSendTakesMessages(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	var got hookLog
	var loop *conn.Loop
	startLoopThen(t, a, func(o *conn.LoopOptions) {
		conn.SetStepHook(o, func(step string) {
			err := loop.Send(context.Background(), ackOf(step))
			got.add("%s: %s, send %v", step, loop.Status().State, errors.Is(err, conn.ErrNoStream))
		})
	}, func(l *conn.Loop) { loop = l })
	h.connected(t, 1)
	waitFor(t, 5*time.Second, "the ack from the connected step", func() bool { return len(h.acks(1)) == 1 })
	h.dropLive(t)
	h.connected(t, 2)
	waitFor(t, 5*time.Second, "three steps", func() bool { return len(got.get()) >= 3 })
	want := []string{"connected: connected, send false", "unpublished: backoff, send true", "connected: connected, send false"}
	if g := got.get()[:3]; !slices.Equal(g, want) {
		t.Errorf("steps %q, want %q", g, want)
	}
}

// F1: a Send from a hook never waits. A watcher fills the queue as soon as
// the status says Connected, before OnConnected runs; OnConnected's own
// sends then fail at once with ErrQueueFull instead of holding the only
// goroutine that drains the queue, and the stream goes on.
func TestASendFromAHookNeverWaits(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	filled := make(chan struct{})
	var res hookLog
	var loop *conn.Loop
	r := startLoopThen(t, a, func(o *conn.LoopOptions) {
		o.OnConnected = func(ctx context.Context, _ uint64, _ conn.HubClock) {
			select {
			case <-filled:
			case <-time.After(5 * time.Second):
				res.add("the watcher never filled the queue")
				return
			}
			start, full := time.Now(), 0
			for k := range conn.QueueSize {
				if err := loop.Send(ctx, ackOf("hook-"+strconv.Itoa(k))); errors.Is(err, conn.ErrQueueFull) {
					full++
				} else if err != nil {
					res.add("send %d: %v", k, err)
				}
			}
			res.add("full %d in under a second %v", full, time.Since(start) < time.Second)
		}
	}, func(l *conn.Loop) { loop = l })
	go func() {
		for r.loop.Status().State != conn.StateConnected {
			time.Sleep(time.Millisecond)
		}
		for k := range conn.QueueSize {
			_ = r.loop.Send(context.Background(), ackOf("w-"+strconv.Itoa(k)))
		}
		close(filled)
	}()
	waitFor(t, 10*time.Second, "OnConnected to finish", func() bool { return len(res.get()) > 0 })
	if g := res.get(); !slices.Equal(g, []string{"full 256 in under a second true"}) {
		t.Fatalf("OnConnected: %q", g)
	}
	waitFor(t, 10*time.Second, "the watcher's acks", func() bool { return len(h.acks(1)) == conn.QueueSize })
	if got := metric(scrape(t, a), "fleet_agent_outbound_blocked_total", `kind="command_ack"`, `result="refused"`); got != "256" {
		t.Errorf("refused hook sends counted %q, want 256", got)
	}
	r.running(t)
}

// connect.hook_blocked says when a hook holds the sender too long.
func TestAHookHoldingTheSenderIsLogged(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	startLoop(t, a, func(o *conn.LoopOptions) {
		conn.SetHookWarn(o, 50*time.Millisecond)
		o.Command = func(context.Context, uint64, *agentv1.Command) bool {
			time.Sleep(300 * time.Millisecond)
			return true
		}
	})
	h.connected(t, 1)
	h.pushCommand(t, "slow", time.Now().Add(time.Hour))
	waitFor(t, 5*time.Second, "connect.hook_blocked", func() bool { return len(a.events(t, "connect.hook_blocked")) == 1 })
	if ev := a.events(t, "connect.hook_blocked")[0]; ev["hook"] != "command" || ev["held_ms"] != float64(50) {
		t.Errorf("connect.hook_blocked = %v", ev)
	}
}
