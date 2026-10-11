package exec_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// A command a handler supports is logged, acked, run once and answered
// once, the ack before the result, and the result sent is the one logged
// (committed before queued). Its message leaves scrubbed, the home folder
// written as ~ and cut to 1 KiB, and its span is a child of Command.trace.
func TestARunIsAckedThenAnsweredOnceFromTheLog(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.result = &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: "read /Users/al/repo with token ghp_" + strings.Repeat("a", 36) + " " + strings.Repeat("x", 3000),
	}
	r := newRig(t, h)
	r.start(t)
	c := cmd("ok-1", 1, "tmux.observe")
	c.Trace = &typev1.TraceContext{Traceparent: "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"}
	r.give(t, c)
	res := r.result(t, "ok-1")
	if res.GetStatus() != typev1.Status_STATUS_OK || h.runs.Load() != 1 {
		t.Fatalf("result %v after %d runs, want OK after one", res, h.runs.Load())
	}
	msg := res.GetMessage()
	if len(msg) > 1024 || !strings.Contains(msg, "~/repo") || strings.Contains(msg, "/Users/al") || strings.Contains(msg, strings.Repeat("a", 36)) {
		t.Errorf("message %q (%d bytes), want scrubbed, home as ~, at most 1 KiB", msg, len(msg))
	}
	tp := res.GetTrace().GetTraceparent()
	if !strings.HasPrefix(tp, "00-0af7651916cd43dd8448eb211c80319c-") || strings.Contains(tp, "b7ad6b7169203331") {
		t.Errorf("result trace %q, want a child span of the command's trace", tp)
	}
	msgs := r.send.all()
	if len(msgs) != 2 || msgs[0].GetCommandAck() == nil || msgs[1].GetCommandResult() == nil {
		t.Fatalf("sent %v, want the ack then the result", msgs)
	}
	e, err := r.db.Command(context.Background(), "ok-1")
	if err != nil {
		t.Fatal(err)
	}
	var logged agentv1.CommandResult
	if err := proto.Unmarshal(e.Result, &logged); err != nil || !proto.Equal(&logged, res) {
		t.Errorf("the sent result differs from the logged one (err %v)", err)
	}
	if got := r.metric(t, "fleet_agent_commands_total", `status="OK"`); got != "1" {
		t.Errorf("commands_total{status=OK} %q, want 1", got)
	}
}

// A known command_id is answered from the log and never run again: a
// finished one with its logged result, an unfinished one with another ack
// while the one run already started sends the one result (ruling fork 4 ii).
func TestAKnownCommandIDIsNeverRunTwice(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.block = make(chan struct{})
	r := newRig(t, h)
	r.start(t)
	c := cmd("k-1", 1, "tmux.observe")
	r.give(t, c)
	waitFor(t, func() bool { return h.runs.Load() == 1 })
	// The hub re-sends it under a new epoch with the same command_id.
	again := proto.Clone(c).(*agentv1.Command) //nolint:forcetypeassert // Clone keeps the type
	again.Epoch, again.Seq = 7, 1
	r.give(t, again)
	waitFor(t, func() bool { acks, _, _ := r.count("k-1"); return acks == 2 })
	close(h.block)
	first := r.result(t, "k-1")
	r.give(t, again)
	waitFor(t, func() bool { _, _, n := r.count("k-1"); return n == 2 })
	if h.runs.Load() != 1 {
		t.Errorf("the handler ran %d times, want once", h.runs.Load())
	}
	var results []*agentv1.CommandResult
	for _, m := range r.send.all() {
		if res := m.GetCommandResult(); res != nil {
			results = append(results, res)
		}
	}
	if len(results) != 2 || !proto.Equal(results[0], first) || !proto.Equal(results[1], first) {
		t.Errorf("results %v, want the logged one twice", results)
	}
}

// Acceptance (ruling fork 11): a command that arrived before a sleep the
// kernel recorded is not started after the wake once its deadline, counted
// with the time asleep, has passed.
func TestACommandIsNotStartedAfterASleepPastItsDeadline(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	r := newRig(t, h)
	r.give(t, cmd("s-1", 1, "tmux.observe")) // queued; the executor is not running yet
	r.clock.sleep(2 * time.Minute)           // the machine slept past the 1 minute deadline
	r.start(t)
	res := r.result(t, "s-1")
	if res.GetStatus() != typev1.Status_STATUS_EXPIRED || h.runs.Load() != 0 {
		t.Errorf("result %v after %d runs, want EXPIRED and never started", res, h.runs.Load())
	}
}

// A wait for local approval survives a stream drop: the executor re-sends
// the AWAITING_LOCAL_CONFIRM progress after each new stream's snapshot
// (ruling fork 4 iv), with the deadline in hub time.
func TestTheApprovalWaitIsReSentOnEachConnect(t *testing.T) {
	t.Parallel()
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
	r := newRig(t, h)
	r.pol.approval["Interrupt"] = true
	r.start(t)
	c := cmd("a-1", 1, "tmux.control")
	c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	c.NotAfterTime = timestamppb.New(hub0.Add(time.Hour))
	r.give(t, c)
	waitFor(t, func() bool { _, p, _ := r.count("a-1"); return p == 1 })
	r.ex.OnConnected(context.Background(), 2, r.clock)
	waitFor(t, func() bool { _, p, _ := r.count("a-1"); return p == 2 })
	for _, m := range r.send.all() {
		if p := m.GetCommandProgress(); p != nil {
			if p.GetStatus() != typev1.Status_STATUS_AWAITING_LOCAL_CONFIRM || p.GetStep() != "awaiting_local_confirm" ||
				p.GetMetadata()["deadline"] != "2026-10-10T13:00:00Z" || p.GetMetadata()["approver"] != "local_user" {
				t.Errorf("progress %v", p)
			}
		}
	}
	if h.runs.Load() != 0 {
		t.Error("the handler ran before an approval")
	}
	if e, err := r.db.Command(context.Background(), "a-1"); err != nil || e.Status != ring.StatusReceived {
		t.Errorf("entry %+v err %v, want received while it waits", e, err)
	}
}

// An approved command runs; the decision is a command.local_approval row.
func TestAnApprovedCommandRuns(t *testing.T) {
	t.Parallel()
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
	r := newRig(t)
	r.buildWith(t, &approver{decide: true}, []*handler{h})
	r.pol.approval["Interrupt"] = true
	r.start(t)
	c := cmd("a-2", 1, "tmux.control")
	c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	r.give(t, c)
	if res := r.result(t, "a-2"); res.GetStatus() != typev1.Status_STATUS_OK || h.runs.Load() != 1 {
		t.Fatalf("result %v after %d runs, want OK after one", res, h.runs.Load())
	}
	rows := r.audit.all()
	if len(rows) != 1 || rows[0].Detail["decision"] != "approved" || rows[0].Detail["channel"] != "cli" {
		t.Errorf("audit rows %+v, want one approved by cli", rows)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// failing is an approver that cannot reach anyone.
type failing struct{ calls atomic.Int32 }

func (f *failing) Await(context.Context, string, string) (bool, string, error) {
	f.calls.Add(1)
	return false, "", errors.New("no approver socket")
}

// An approver that fails at once is no decision: the wait still ends at
// the deadline, without asking it in a tight loop.
func TestAFailingApproverDoesNotSpin(t *testing.T) {
	t.Parallel()
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
	r := newRig(t)
	a := &failing{}
	var list []exec.Handler
	r.build(t, a, append(list, h)...)
	r.pol.approval["Interrupt"] = true
	r.start(t)
	c := cmd("f-1", 1, "tmux.control")
	c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	c.NotAfterTime = timestamppb.New(hub0.Add(2 * time.Second))
	r.give(t, c)
	res := r.result(t, "f-1")
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_LOCAL_CONFIRM_TIMEOUT || h.runs.Load() != 0 {
		t.Errorf("result %v after %d runs, want LOCAL_CONFIRM_TIMEOUT, never run", res, h.runs.Load())
	}
	if n := a.calls.Load(); n > 3 {
		t.Errorf("the approver was asked %d times in 2 s, want at most a few", n)
	}
}

// stepper reports one step, then changes its own progress and waits.
type stepper struct {
	handler
	reported chan struct{}
}

func (s *stepper) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	p := &agentv1.CommandProgress{Step: "wait_ready", StepIndex: 3, StepCount: 14, Status: typev1.Status_STATUS_OK}
	if err := r.Progress(ctx, p); err != nil {
		return nil
	}
	p.Step = "changed after reporting"
	close(s.reported)
	<-ctx.Done()
	return nil
}

// The executor keeps its own copy of a reported step: a handler that
// changes its progress afterwards does not change what is re-sent on the
// next connect, nor race the envelope goroutine that re-sends it.
func TestAReportedStepIsCopied(t *testing.T) {
	t.Parallel()
	h := &stepper{handler: handler{kind: "StartGoal", caps: []*typev1.Capability{{Name: "goal.start", Version: 1}}}, reported: make(chan struct{})}
	r := newRig(t, h)
	r.start(t)
	c := cmd("st-1", 1, "goal.start")
	c.Kind = &agentv1.Command_StartGoal{StartGoal: &agentv1.StartGoal{}}
	r.give(t, c)
	<-h.reported
	r.ex.OnConnected(context.Background(), 2, r.clock)
	waitFor(t, func() bool { _, p, _ := r.count("st-1"); return p == 2 })
	for _, m := range r.send.all() {
		if p := m.GetCommandProgress(); p != nil && p.GetStep() != "wait_ready" {
			t.Errorf("progress step %q sent, want the reported wait_ready", p.GetStep())
		}
	}
	if e, err := r.db.Command(context.Background(), "st-1"); err != nil || e.Step != 3 {
		t.Errorf("entry %+v err %v, want step 3 in the log", e, err)
	}
}
