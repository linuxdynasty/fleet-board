package exec

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// approvalCheck bounds one wait for the approver, so the deadline is
// re-read on the strict clock (which counts sleep) at least this often.
const approvalCheck = 30 * time.Second

// do is one command's goroutine: it records a refusal, or waits for local
// approval and runs the handler, then records and sends the one result. A
// run cut off by the end of Run leaves the entry for the next run's resume.
func (e *Executor) do(ctx context.Context, c *agentv1.Command, ent ring.Entry, p plan) {
	defer e.wg.Done()
	defer e.release(ent.ID)
	ctx, carried := parent(ctx, c)
	ctx, s := e.m.tracer.Start(ctx, "agent.command", trace.WithAttributes(attribute.String("kind", ent.Kind)))
	defer s.End()
	sp := &span{s: s, carried: carried}
	if p.refusal != nil {
		e.finish(ctx, c, ent, p.refusal, time.Time{}, sp)
		return
	}
	started := e.opts.Now()
	if p.approval {
		r, cut := e.await(ctx, c, ent)
		if cut {
			return
		}
		if r != nil {
			e.finish(ctx, c, ent, r, started, sp)
			return
		}
	}
	class := p.handler.Class(c)
	if p.resume {
		e.log.InfoContext(ctx, "command.resumed", "command_id", ent.ID, "kind", ent.Kind, "class", class.String())
		if class == NotRepeatSafe {
			e.finish(ctx, c, ent, &agentv1.CommandResult{
				Status:  typev1.Status_STATUS_UNKNOWN_OUTCOME,
				Message: "the agent restarted while the command ran and cannot tell whether it took effect",
			}, started, sp)
			return
		}
	}
	if err := e.opts.DB.SetRunning(ctx, ent.ID, ent.Step); err != nil {
		e.storeFailed(ctx, ent.ID, "running", err)
		return
	}
	req := Request{
		Command: c, Step: ent.Step, FleetOnly: p.cell.fleetOnly, FleetOrMarked: p.cell.fleetOrMarked,
		Progress: e.progress(ent.ID),
	}
	var r *agentv1.CommandResult
	if p.resume && class == PerStep {
		r = p.handler.Resume(ctx, req)
	} else {
		r = p.handler.Run(ctx, req)
	}
	if ctx.Err() != nil {
		return
	}
	if r == nil {
		r = &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED, Message: "the handler returned no result"}
	}
	e.finish(ctx, c, ent, r, started, sp)
}

// await sends the AWAITING_LOCAL_CONFIRM progress and waits for the person
// at the machine until the deadline in strict hub time (02-contracts; 02g).
// It returns nil when approved, a refusal when denied or timed out, and cut
// when Run ends first.
func (e *Executor) await(ctx context.Context, c *agentv1.Command, ent ring.Entry) (*agentv1.CommandResult, bool) {
	deadline := c.GetNotAfterTime().AsTime()
	p := &agentv1.CommandProgress{
		CommandId: c.GetCommandId(), Step: "awaiting_local_confirm",
		Status: typev1.Status_STATUS_AWAITING_LOCAL_CONFIRM, ReportTime: timestamppb.New(e.opts.Now()),
		Metadata: map[string]string{"deadline": deadline.UTC().Format(time.RFC3339), "approver": "local_user"},
	}
	e.setProgress(ent.ID, p)
	e.send(ctx, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{CommandProgress: p}})
	for {
		now, ok := e.opts.Clock.Now()
		if !ok || !now.Before(deadline) {
			return refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_LOCAL_CONFIRM_TIMEOUT,
				"nobody approved the command at the machine before its deadline"), false
		}
		wctx, cancel := context.WithTimeout(ctx, min(deadline.Sub(now), approvalCheck))
		var approved, decided bool
		var channel string
		if a := e.opts.Approver; a != nil {
			var err error
			approved, channel, err = a.Await(wctx, ent.ID, ent.Kind)
			decided = err == nil
		} else {
			<-wctx.Done()
		}
		cancel()
		if ctx.Err() != nil {
			return nil, true
		}
		if !decided {
			continue
		}
		decision := "denied"
		if approved {
			decision = "approved"
		}
		e.audit(audit.Entry{
			Action: audit.CommandLocalApproval, CommandID: ent.ID, Kind: ent.Kind,
			RequestedBy: principal(c.GetRequestedBy()), Detail: map[string]string{"decision": decision, "channel": closedOr(channel)},
		})
		if approved {
			return nil, false
		}
		return refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_LOCAL_DENIED,
			"the person at the machine denied the command"), false
	}
}

// progress is the Request.Progress of one command: it marks the step in
// the log, keeps the progress for a re-send and queues it.
func (e *Executor) progress(id string) func(context.Context, *agentv1.CommandProgress) error {
	return func(ctx context.Context, p *agentv1.CommandProgress) error {
		p.CommandId = &typev1.CommandId{Value: id}
		p.Message = e.text(p.GetMessage())
		if err := e.opts.DB.SetRunning(ctx, id, int(p.GetStepIndex())); err != nil {
			return err
		}
		e.setProgress(id, p)
		e.send(ctx, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{CommandProgress: p}})
		return nil
	}
}
