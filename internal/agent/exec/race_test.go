package exec_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// heldOpen is agent.db whose Open signals once it has read the entries and
// then waits, so a test can finish a command between the read and the pass.
type heldOpen struct {
	exec.Store
	read    chan struct{}
	release chan struct{}
}

func (h heldOpen) Open(ctx context.Context) ([]ring.Entry, error) {
	es, err := h.Store.Open(ctx)
	close(h.read)
	<-h.release
	return es, err
}

// gate is an approver that counts its calls and answers once opened.
type gate struct {
	calls  atomic.Int32
	called chan struct{}
	open   chan struct{}
}

func (g *gate) Await(ctx context.Context, _, _ string) (bool, string, error) {
	if g.calls.Add(1) == 1 {
		close(g.called)
	}
	select {
	case <-g.open:
		return true, "cli", nil
	case <-ctx.Done():
		return false, "", ctx.Err()
	}
}

// A command this run took can finish while the resume pass holds a stale
// list of open entries. The pass must not start it again: one approval,
// one audit row, one run, no command.resumed and no store failure.
func TestTheResumePassSkipsACommandThatFinishedMeanwhile(t *testing.T) {
	t.Parallel()
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}, class: exec.RepeatSafe}
	r := newRig(t)
	g := &gate{called: make(chan struct{}), open: make(chan struct{})}
	held := heldOpen{Store: r.db, read: make(chan struct{}), release: make(chan struct{})}
	ex, err := exec.New(exec.Options{
		DB: held, Sender: r.send, Clock: r.clock, Policy: r.pol, Audit: r.audit, Approver: g,
		Handlers: []exec.Handler{h, capturePane("tmux.observe")}, Telemetry: r.tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	r.pol.approval["Interrupt"] = true
	r.start(t)

	c := cmd("race-1", 1, "tmux.control")
	c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	c.NotAfterTime = timestamppb.New(hub0.Add(time.Hour))
	r.give(t, c)
	<-g.called                                         // waiting for approval: received in the log
	r.ex.OnConnected(context.Background(), 1, r.clock) // the pass reads race-1 as open...
	<-held.read
	close(g.open) // ...and it is approved, runs and finishes meanwhile
	if res := r.result(t, "race-1"); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("result %v, want OK", res)
	}
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	close(held.release)
	// The intake is read only after the pass returns: once this is answered,
	// the pass is over and anything it started has been claimed.
	r.give(t, cmd("after", 2, "tmux.observe"))
	r.result(t, "after")
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })

	if n := g.calls.Load(); n != 1 {
		t.Errorf("the approver was asked %d times, want 1", n)
	}
	if n := h.runs.Load(); n != 1 {
		t.Errorf("the handler ran %d times, want 1", n)
	}
	approvals := 0
	for _, row := range r.audit.all() {
		if row.Action == audit.CommandLocalApproval {
			approvals++
		}
	}
	if approvals != 1 {
		t.Errorf("%d local_approval audit rows, want 1", approvals)
	}
	for _, ev := range []string{"command.resumed", "command.store_failed"} {
		if got := conntest.Lines(t, r.logs.String(), ev); len(got) != 0 {
			t.Errorf("%s logged %v, want none", ev, got)
		}
	}
	if _, _, n := r.count("race-1"); n != 1 {
		t.Errorf("%d results for race-1, want 1", n)
	}
}

// The same race with the command running when the pass reads the log: no
// second run and no command.resumed.
func TestTheResumePassSkipsARunThatFinishedMeanwhile(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	h.block = make(chan struct{})
	r := newRig(t)
	held := heldOpen{Store: r.db, read: make(chan struct{}), release: make(chan struct{})}
	ex, err := exec.New(exec.Options{
		DB: held, Sender: r.send, Clock: r.clock, Policy: r.pol, Audit: r.audit,
		Handlers: []exec.Handler{h}, Telemetry: r.tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	r.start(t)
	r.give(t, cmd("race-2", 1, "tmux.observe"))
	waitFor(t, func() bool { return h.runs.Load() == 1 }) // running in the log
	r.ex.OnConnected(context.Background(), 1, r.clock)
	<-held.read
	close(h.block)
	r.result(t, "race-2")
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	close(held.release)
	r.give(t, cmd("after", 2, "tmux.observe"))
	r.result(t, "after")
	waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
	if n := h.runs.Load(); n != 2 { // race-2 once, after once
		t.Errorf("the handler ran %d times, want 2 (race-2 once and after once)", n)
	}
	for _, ev := range []string{"command.resumed", "command.store_failed"} {
		if got := conntest.Lines(t, r.logs.String(), ev); len(got) != 0 {
			t.Errorf("%s logged %v, want none", ev, got)
		}
	}
}
