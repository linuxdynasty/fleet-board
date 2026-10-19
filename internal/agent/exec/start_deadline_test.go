package exec_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// delayedRunning completes the real durable write, then models time spent
// waiting for that write. No database transaction is held during the clock jump.
type delayedRunning struct {
	exec.Store
	after func()
}

func (d delayedRunning) SetRunning(ctx context.Context, id string, step int) error {
	if err := d.Store.SetRunning(ctx, id, step); err != nil {
		return err
	}
	d.after()
	return nil
}

func TestACommandCannotStartAfterItsRunningWritePassesDeadline(t *testing.T) {
	for _, lostClock := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "unknown_clock"}[lostClock], func(t *testing.T) {
			h := capturePane("tmux.observe")
			r := newRig(t, h)
			db := delayedRunning{Store: r.db, after: func() {
				if lostClock {
					r.clock.bad.Store(true)
				} else {
					r.clock.sleep(2 * time.Minute)
				}
			}}
			ex, err := exec.New(exec.Options{
				DB: db, Sender: r.send, Clock: r.clock, Policy: r.pol,
				Audit: r.audit, Handlers: []exec.Handler{h}, Telemetry: r.tel,
			})
			if err != nil {
				t.Fatal(err)
			}
			r.ex = ex
			r.start(t)
			r.give(t, cmd("late-write", 1, "tmux.observe"))
			res := r.result(t, "late-write")
			if res.GetStatus() != typev1.Status_STATUS_EXPIRED || h.runs.Load() != 0 {
				t.Fatalf("result %v after %d handler runs; want EXPIRED without running", res, h.runs.Load())
			}
			if got := r.metric(t, "fleet_agent_commands_refused_total", `reason="EXPIRED"`); got != "1" {
				t.Fatalf("expiry refusal count %q; want 1", got)
			}
		})
	}
}

type lateApproval struct{ clock *hubClock }

func (a lateApproval) Await(context.Context, string, string) (bool, string, error) {
	a.clock.sleep(2 * time.Minute)
	return true, "cli", nil
}

func TestAnApprovalAfterTheDeadlineCannotStartTheHandler(t *testing.T) {
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
	r := newRig(t, h)
	r.pol.approval["Interrupt"] = true
	r.build(t, lateApproval{clock: r.clock}, h)
	r.start(t)
	c := cmd("late-approval", 1, "tmux.control")
	c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	r.give(t, c)
	res := r.result(t, "late-approval")
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_LOCAL_CONFIRM_TIMEOUT || h.runs.Load() != 0 {
		t.Fatalf("result %v after %d handler runs; want LOCAL_CONFIRM_TIMEOUT without running", res, h.runs.Load())
	}
	if got := r.metric(t, "fleet_agent_commands_refused_total", `reason="LOCAL_CONFIRM_TIMEOUT"`); got != "1" {
		t.Fatalf("late approval refusal count %q; want 1", got)
	}
}
