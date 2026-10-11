package exec_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// failOnce is agent.db whose first RecordResult fails, as a full disk or a
// busy file would: the entry stays running with no goroutine holding it.
type failOnce struct {
	exec.Store
	failed atomic.Bool
}

func (f *failOnce) RecordResult(ctx context.Context, id string, b []byte, at time.Time) error {
	if f.failed.CompareAndSwap(false, true) {
		return errors.New("disk full")
	}
	return f.Store.RecordResult(ctx, id, b, at)
}

// 6e round-2 ruling D: the resume pass runs at every connect, so an entry
// a failed store write left behind is taken up again without a restart: a
// repeat-safe command runs again and sends its one result; a command that
// is not repeat safe ends UNKNOWN_OUTCOME.
func TestAnEntryLeftByAFailedWriteIsTakenUpAtTheNextConnect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		class exec.Class
		runs  int32
		want  typev1.Status
	}{
		{exec.RepeatSafe, 2, typev1.Status_STATUS_OK},
		{exec.NotRepeatSafe, 1, typev1.Status_STATUS_UNKNOWN_OUTCOME},
	} {
		t.Run(tc.class.String(), func(t *testing.T) {
			t.Parallel()
			h := capturePane("tmux.observe")
			h.class = tc.class
			r := newRig(t)
			ex, err := exec.New(exec.Options{
				DB: &failOnce{Store: r.db}, Sender: r.send, Clock: r.clock, Policy: r.pol,
				Handlers: []exec.Handler{h}, Telemetry: r.tel,
			})
			if err != nil {
				t.Fatal(err)
			}
			r.ex = ex
			r.start(t)
			r.ex.OnConnected(context.Background(), 1, r.clock) // the first pass: nothing open
			r.give(t, cmd("lost", 1, "tmux.observe"))
			waitFor(t, func() bool { return r.metric(t, "fleet_agent_command_store_failed_total", `step="result"`) == "1" })
			waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
			if _, _, n := r.count("lost"); n != 0 {
				t.Fatalf("%d results after the failed write, want none", n)
			}
			r.ex.OnConnected(context.Background(), 2, r.clock) // the next stream
			if res := r.result(t, "lost"); res.GetStatus() != tc.want {
				t.Errorf("result %v, want %v", res, tc.want)
			}
			waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
			if n := h.runs.Load(); n != tc.runs {
				t.Errorf("the handler ran %d times, want %d", n, tc.runs)
			}
			if _, _, n := r.count("lost"); n != 1 {
				t.Errorf("%d results, want 1", n)
			}
		})
	}
}

// A running entry a goroutine holds is never started by a pass, however
// many connects there are.
func TestConnectsNeverStartAHeldRunAgain(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.block = make(chan struct{})
	r := newRig(t, h)
	r.start(t)
	r.give(t, cmd("held", 1, "tmux.observe"))
	waitFor(t, func() bool { return h.runs.Load() == 1 })
	r.ex.OnConnected(context.Background(), 1, r.clock)
	r.ex.OnConnected(context.Background(), 2, r.clock)
	r.give(t, cmd("after", 2, "tmux.control")) // answered only after the passes
	r.result(t, "after")
	close(h.block)
	r.result(t, "held")
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	if n := h.runs.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1", n)
	}
	if _, _, n := r.count("held"); n != 1 {
		t.Errorf("%d results, want 1", n)
	}
}

// failFor is agent.db whose RecordResult fails once for one command_id.
type failFor struct {
	exec.Store
	id     string
	failed atomic.Bool
}

func (f *failFor) RecordResult(ctx context.Context, id string, b []byte, at time.Time) error {
	if id == f.id && f.failed.CompareAndSwap(false, true) {
		return errors.New("disk full")
	}
	return f.Store.RecordResult(ctx, id, b, at)
}

// A refused command whose result write failed is still received: it was
// never admitted, so the resume pass runs the rate check on it again
// (counting it once, as it is already in the window) instead of running
// it. Only a running entry, already admitted, skips the window.
func TestAResumedReceivedEntryIsRateCheckedAgain(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	r := newRig(t)
	ex, err := exec.New(exec.Options{
		DB: &failFor{Store: r.db, id: "r-2"}, Sender: r.send, Clock: r.clock, Policy: r.pol,
		Handlers: []exec.Handler{h}, Telemetry: r.tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	r.pol.limits["CapturePane"] = 1
	r.start(t)
	r.give(t, cmd("r-1", 1, "tmux.observe"))
	r.result(t, "r-1")
	r.give(t, cmd("r-2", 2, "tmux.observe")) // RATE_LIMITED, but its result write fails
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_command_store_failed_total", `step="result"`) == "1" })
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	r.ex.OnConnected(context.Background(), 2, r.clock)
	if res := r.result(t, "r-2"); res.GetReason() != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Errorf("after the next connect: %v, want RATE_LIMITED", res)
	}
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	if n := h.runs.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1 (r-1 only)", n)
	}
}
