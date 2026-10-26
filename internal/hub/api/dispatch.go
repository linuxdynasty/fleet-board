package api

import (
	"context"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// dispatchAuditAction is the audit action of a manual dispatch (02m).
const dispatchAuditAction = "goal.start"

// preflightAuditAction is the audit action of a PreflightGoal call: the
// hub checks plus the agent command it sent, like a start that only
// previews.
const preflightAuditAction = "goal.preflight"

// dispatchFacts is everything one manual dispatch gathers in its writer
// transaction before running the hub checks (03c planning rules, 03h): the
// work item, the branch, the consent, the machine and its load, the
// account, and the integration the item comes from. The sessionID and
// GoalSpec fields are what the agent command carries.
type dispatchFacts struct {
	item           statedb.WorkItemState
	itemKey        string
	branch         string
	display        string
	consent        []typev1.ConsentAct
	merge          bool
	machine        statedb.MachineDispatch
	machineID      string
	harness        string
	accountID      string
	checks         []*hubv1.PreflightCheck
	decision       goals.Decision
	sessionID      string
	repoHost       string
	repoPath       string
	goalText       string
	model          string
	effort         string
	fresh          bool
	profile        string
	changeRequest  *typev1.WorkItemRef
	workItemRef    *typev1.WorkItemRef
	dispatchReason string
}

// PreflightGoal runs the hub's checks and the agent's Preflight without
// starting anything, for the same input StartGoal would take (03c "The
// same dispatch through the API"). An empty placement asks the scheduler
// where it would run: row 22 first-fits the machine and requires harness
// and account explicitly (row 23 ranks them). The agent command it sends
// is audited as goal.preflight.
func (f *Fleet) PreflightGoal(ctx context.Context, req *connect.Request[hubv1.PreflightGoalRequest],
) (*connect.Response[hubv1.PreflightGoalResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.dispatchRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.dispatchRefusedAs(ctx, actor, err)
	}
	in := m.GetInput()
	if err := checkGoalInput(in); err != nil {
		return nil, f.dispatchRefusedAs(ctx, actor, err)
	}
	now := f.hubNow()
	var facts *dispatchFacts
	var queued outbox.Op
	var out *hubv1.Operation
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		fx, err := f.gatherDispatchFacts(ctx, tx, who, in, nil, now)
		if err != nil {
			return err
		}
		facts = fx
		if fx.decision.Outcome == goals.StartBlocked && fx.machineID == "" {
			// No machine fits, so no agent preflight runs: the hub checks
			// alone answer what would happen.
			return nil
		}
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_Preflight{Preflight: &agentv1.Preflight{
				Spec:      fx.agentSpec(""),
				ReusePane: in.GetPlacement().GetReusePane(),
			}},
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		op, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: fx.machineID, Command: cmd, RequestID: m.GetRequestId(), Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		if err := logRequest(ctx, tx, actor, m.GetRequestId(), op.CommandID, dispatchHash(m), now); err != nil {
			return err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: preflightAuditAction, Target: "work/" + fx.itemKey,
			CommandID: op.CommandID, RequestID: m.GetRequestId(), Now: now,
		}); err != nil {
			return err
		}
		out, err = operationFromOp(ctx, tx, op, fx.machineID, who, m.GetRequestId(), now)
		if err != nil {
			return err
		}
		queued = op
		return nil
	}, &dispAudits), func() {
		if queued.Created {
			f.publishOperationWatch(queued.HubSeq, out)
		}
		f.publishAudits(dispAudits)
	})
	if submitErr != nil {
		return nil, f.dispatchStoreError(ctx, actor, submitErr)
	}
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.dispatchStoreError(ctx, actor, commitErr)
	}
	if queued.Created && f.onCommandQueued != nil {
		f.onCommandQueued(queued)
	}
	resp := &hubv1.PreflightGoalResponse{Operation: out, HubChecks: facts.checks}
	if facts.machineID != "" {
		resp.Placement = &hubv1.GoalPlacement{
			Machine: &typev1.MachineId{Value: facts.machineID},
			Harness: &typev1.Harness{Name: facts.harness},
			Account: &typev1.AccountId{Value: facts.accountID},
		}
	}
	f.countDispatch(ctx, "preflight", dispatchResult(facts.decision))
	f.tel.Logger.InfoContext(ctx, "dispatch.preflight", "actor", actor, "work_item", facts.itemKey,
		"machine", facts.machineID, "checks", len(facts.checks))
	return connect.NewResponse(resp), nil
}

// StartGoal dispatches a goal (manual dispatch, 03c "Start, accept then
// send then run then confirm"). It re-runs the hub checks: a BLOCK fails
// FAILED_PRECONDITION with the first one's reason (every BLOCK in
// violations), an unacknowledged WARN fails
// PREFLIGHT_WARNINGS_UNACKNOWLEDGED, and a CONFIRM returns a
// DISPATCH_CONFIRM approval and starts nothing until it is approved (02m).
// Otherwise one transaction inserts the STARTING goal with its run 1 and
// first transition, moves the work item to DISPATCHED, enqueues StartGoal
// and audits goal.start.
func (f *Fleet) StartGoal(ctx context.Context, req *connect.Request[hubv1.StartGoalRequest],
) (*connect.Response[hubv1.StartGoalResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.dispatchRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.dispatchRefusedAs(ctx, actor, err)
	}
	in := m.GetInput()
	if err := checkGoalInput(in); err != nil {
		return nil, f.dispatchRefusedAs(ctx, actor, err)
	}
	now := f.hubNow()
	var resp *hubv1.StartGoalResponse
	var queued outbox.Op
	var goalAfter statedb.GoalRow
	var itemAfter statedb.WorkItemRow
	var apprAfter *hubv1.Approval
	var apprSeq int64
	var machineID string
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		if id, approvalID, replay, err := f.checkDispatchReplay(ctx, tx, actor, m); err != nil || replay {
			if err != nil {
				return err
			}
			if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
				return err
			}
			if approvalID != "" {
				apprAfter, err = approvals.Get(ctx, tx, approvalID, now)
				if err != nil {
					return err
				}
				resp = &hubv1.StartGoalResponse{Approval: &hubv1.ApprovalId{Value: approvalID}}
				return nil
			}
			stored, err := loadOperation(ctx, tx, id)
			if err != nil {
				return err
			}
			goalID, err := dispatchCommandGoal(ctx, tx, id)
			if err != nil {
				return err
			}
			resp = &hubv1.StartGoalResponse{Goal: &typev1.GoalId{Value: goalID}, Operation: stored}
			return nil
		}
		facts, err := f.gatherDispatchFacts(ctx, tx, who, in, m.GetAcknowledgedWarnings(), now)
		if err != nil {
			return err
		}
		machineID = facts.machineID
		switch facts.decision.Outcome {
		case goals.StartBlocked:
			return blockError(facts.decision)
		case goals.StartWarnUnacked:
			return warnUnackedError(facts.decision)
		case goals.StartConfirm:
			approval, seq, err := f.raiseDispatchConfirm(ctx, tx, who, m, in, facts, now)
			if err != nil {
				return err
			}
			apprAfter, apprSeq = approval, seq
			resp = &hubv1.StartGoalResponse{Approval: approval.GetId()}
			return nil
		default:
			// StartOK (and any future go-ahead outcome) proceeds to dispatch below.
		}
		goalID, err := ulid.New(now, f.random)
		if err != nil {
			return fmt.Errorf("dispatch: mint goal id: %w", err)
		}
		after, goalOp, op, err := f.insertDispatchTx(ctx, tx, who, m, facts, goalID, now)
		if err != nil {
			return err
		}
		goalAfter, queued = after, op
		itemAfter, err = readWorkItemForPublish(ctx, tx, facts.itemKey)
		if err != nil {
			return err
		}
		resp = &hubv1.StartGoalResponse{
			Goal:      &typev1.GoalId{Value: goalID},
			Operation: goalOp,
		}
		return nil
	}, &dispAudits), func() {
		if apprAfter != nil && resp.GetOperation() == nil {
			f.publishApprovalWatch(apprSeq, apprAfter, hubv1.ChangeType_CHANGE_TYPE_CREATED, false)
		}
		if queued.Created {
			f.publishOperationWatch(queued.HubSeq, resp.GetOperation())
			f.PublishGoalsCommitted(ctx, goalAfter.MachineID, nil, []statedb.GoalRow{goalAfter})
			f.publishDispatchWorkItem(ctx, itemAfter)
		}
		f.publishAudits(dispAudits)
	})
	if submitErr != nil {
		return nil, f.dispatchStoreError(ctx, actor, submitErr)
	}
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.dispatchStoreError(ctx, actor, commitErr)
	}
	if queued.Created && f.onCommandQueued != nil {
		f.onCommandQueued(queued)
	}
	mode := "start"
	if resp.GetApproval() != nil {
		mode = "confirm"
	}
	f.countDispatch(ctx, mode, dispatchResultOf(resp))
	f.tel.Logger.InfoContext(ctx, "dispatch.planned", "actor", actor,
		"goal", resp.GetGoal().GetValue(), "work_item", respGoalWorkItem(resp, in),
		"machine", machineID, "mode", mode)
	return connect.NewResponse(resp), nil
}
