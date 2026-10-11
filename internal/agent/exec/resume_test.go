package exec_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// logged writes c to agent.db as a previous run left it: received, or
// running at step.
func logged(t *testing.T, r *rig, c *agentv1.Command, running bool, step int) {
	t.Helper()
	ctx := context.Background()
	body, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.db.LogAndApply(ctx, ring.Command{
		ID: c.GetCommandId().GetValue(), Kind: kindName(c), Epoch: c.GetEpoch(), Seq: c.GetSeq(),
		Received: time.Now(), NotAfter: c.GetNotAfterTime().AsTime(), Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	if running {
		if err := r.db.SetRunning(ctx, c.GetCommandId().GetValue(), step); err != nil {
			t.Fatal(err)
		}
	}
}

func kindName(c *agentv1.Command) string {
	return string(c.ProtoReflect().WhichOneof(c.ProtoReflect().Descriptor().Oneofs().ByName("kind")).Message().Name())
}

// Restart recovery (ruling fork 4 iii): nothing is resumed before the first
// stream's snapshot is out (hub time is unknown until then); then a received
// entry is deadline-checked and runs, a running entry past its deadline
// ends EXPIRED, and a running one inside it is run again (repeat safe),
// ended UNKNOWN_OUTCOME (not repeat safe) or resumed at its step (per
// step). The pass runs at every connect, and neither a second connect nor
// a hub re-send that races it ever starts a second run.
func TestRestartResumesEachClassOnce(t *testing.T) {
	t.Parallel()
	safe := capturePane("tmux.observe")
	unsafe := &handler{kind: "SendKeys", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}, class: exec.NotRepeatSafe}
	steps := &handler{kind: "StartGoal", caps: []*typev1.Capability{{Name: "goal.start", Version: 1}}, class: exec.PerStep}
	fresh := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}, class: exec.RepeatSafe}
	r := newRig(t, safe, unsafe, steps, fresh)

	received := cmd("received", 1, "tmux.control")
	received.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	logged(t, r, received, false, 0)
	logged(t, r, cmd("running-safe", 2, "tmux.observe"), true, 0)
	sendKeys := cmd("running-unsafe", 3, "tmux.control")
	sendKeys.Kind = &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{Purpose: typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT}}
	logged(t, r, sendKeys, true, 0)
	goal := cmd("running-steps", 4, "goal.start")
	goal.Kind = &agentv1.Command_StartGoal{StartGoal: &agentv1.StartGoal{}}
	logged(t, r, goal, true, 3)
	late := cmd("running-late", 5, "tmux.observe")
	late.NotAfterTime = timestamppb.New(hub0.Add(-time.Second))
	logged(t, r, late, true, 0)

	r.start(t)
	r.give(t, received) // the hub re-sends the received one before the pass
	waitFor(t, func() bool { acks, _, _ := r.count("received"); return acks == 1 })
	if fresh.runs.Load() != 0 || len(r.send.all()) != 1 {
		t.Fatalf("before the first snapshot: %d runs, sent %v; want only the re-ack", fresh.runs.Load(), r.send.all())
	}
	r.ex.OnConnected(context.Background(), 1, r.clock)
	for id, want := range map[string]typev1.Status{
		"received": typev1.Status_STATUS_OK, "running-safe": typev1.Status_STATUS_OK,
		"running-unsafe": typev1.Status_STATUS_UNKNOWN_OUTCOME, "running-steps": typev1.Status_STATUS_OK,
		"running-late": typev1.Status_STATUS_EXPIRED,
	} {
		res := r.result(t, id)
		keep(res.GetMessage())
		if got := res.GetStatus(); got != want {
			t.Errorf("%s: %v, want %v", id, got, want)
		}
	}
	r.ex.OnConnected(context.Background(), 2, r.clock)
	r.give(t, received)
	waitFor(t, func() bool { _, _, n := r.count("received"); return n == 2 })
	if fresh.runs.Load() != 1 || safe.runs.Load() != 1 || unsafe.runs.Load() != 0 || steps.runs.Load() != 0 ||
		steps.resumes.Load() != 1 || steps.step.Load() != 3 {
		t.Errorf("runs: received %d, safe %d, unsafe %d, steps %d run %d resumed at %d; want 1, 1, 0, 0, 1 at 3",
			fresh.runs.Load(), safe.runs.Load(), unsafe.runs.Load(), steps.runs.Load(), steps.resumes.Load(), steps.step.Load())
	}
	open, err := r.db.Open(context.Background())
	if err != nil || len(open) != 0 {
		t.Errorf("open entries after the pass %+v (err %v), want none", open, err)
	}
}

// A run cut off by the agent stopping leaves its entry running, with no
// result, for the resume pass to take up.
func TestStoppingLeavesARunToResume(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.block = make(chan struct{})
	r := newRig(t, h)
	r.start(t)
	r.give(t, cmd("cut-1", 1, "tmux.observe"))
	waitFor(t, func() bool { return h.runs.Load() == 1 })
	r.stop()
	e, err := r.db.Command(context.Background(), "cut-1")
	if err != nil || e.Status != ring.StatusRunning {
		t.Errorf("entry %+v err %v, want running with no result", e, err)
	}
	if _, _, n := r.count("cut-1"); n != 0 {
		t.Errorf("%d results for a cut run, want none", n)
	}
}

// A logged command the agent cannot read back ends UNKNOWN_OUTCOME after a
// restart, and a handler that returns nothing ends FAILED: neither leaves
// the command without its one result.
func TestUnreadableEntriesAndEmptyResultsStillGetOneResult(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	r := newRig(t, h)
	if _, _, err := r.db.LogAndApply(context.Background(), ring.Command{
		ID: "garbled", Kind: "CapturePane", Epoch: 1, Seq: 1, Received: time.Now(),
		NotAfter: hub0.Add(time.Minute), Body: []byte{0xff, 0xff, 0xff},
	}); err != nil {
		t.Fatal(err)
	}
	h.nilResult = true
	r.start(t)
	r.ex.OnConnected(context.Background(), 1, r.clock)
	res := r.result(t, "garbled")
	keep(res.GetMessage())
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME || h.runs.Load() != 0 {
		t.Errorf("garbled entry: %v after %d runs, want UNKNOWN_OUTCOME, never run", res, h.runs.Load())
	}
	r.give(t, cmd("empty", 2, "tmux.observe"))
	res = r.result(t, "empty")
	keep(res.GetMessage())
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetMessage() == "" {
		t.Errorf("a handler with no result: %v, want FAILED with a message", res)
	}
}
