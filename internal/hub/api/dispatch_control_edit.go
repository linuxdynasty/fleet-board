package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// applyEdit re-sends StartGoal with a new objective (or re-arms a
// finished goal with it). The spec version moves +1 and the stored
// definition updates in the same transaction; expected_version guards the
// read (0 skips the check). VERIFYING moves to DISAGREE: the old claim is
// superseded by new work.
func (f *Fleet) applyEdit(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	if m.GetText() == "" {
		return statedb.GoalRow{}, nil, nil, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "text is required for EDIT", nil,
			[]*typev1.FieldViolation{{Field: "text", Description: "the new objective is required"}})
	}
	//nolint:gosec // G115: versions count up from 0, never negative.
	if want := m.GetExpectedVersion(); want != 0 && want != uint64(ctl.row.Version) {
		return statedb.GoalRow{}, nil, nil, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the goal changed; read it again", map[string]string{"current_version": fmt.Sprintf("%d", ctl.row.Version)}, nil)
	}
	def := cloneGoalDef(ctl.spec)
	def.GoalText = m.GetText()
	raw, err := protojson.Marshal(def)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode edited spec: %w", err)
	}
	version := ctl.row.Version + 1
	specJSON := string(raw)
	to := ctl.row.Phase
	if ctl.status.GetPhase() == hubv1.GoalPhase_GOAL_PHASE_VERIFYING {
		to = "GOAL_PHASE_DISAGREE"
	}
	op, err := f.enqueueControlCmd(ctx, mk, &agentv1.Command{
		Kind: &agentv1.Command_StartGoal{StartGoal: &agentv1.StartGoal{
			Spec:            controlAgentSpec(ctl, def),
			ReusePane:       ctl.status.GetPane(),
			ReadyTimeout:    readyTimeout(f),
			GoalFileTimeout: goalFileTimeout(f),
		}},
	}, rearmAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	status := cloneGoalStatus(ctl.status)
	status.LastOperation = &typev1.CommandId{Value: op.CommandID}
	statusJSON, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode edit status: %w", err)
	}
	after, err := f.moveControlGoal(ctx, mk.tx, ctl, statedb.GoalTransition{
		To: to, Reason: "GOAL_PHASE_REASON_UNSPECIFIED", Status: string(statusJSON),
		Actor: mk.who.GetId().GetValue(), CommandID: op.CommandID,
		NewRunCause: "GOAL_RUN_CAUSE_REARM", NewRunMachine: ctl.row.MachineID,
		Spec: &specJSON, Version: &version,
		Fingerprint: goals.Fingerprint(status.GetLineState(), status.GetFirstLine(), false,
			status.GetHeadCommit(), to, ""),
		NowMs: mk.now.UnixMilli(),
	}, actorOf(mk.who), rearmAuditAction, mk.now.UnixMilli())
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.stageControlItem(ctx, mk.tx, ctl, after); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.logControlRequest(ctx, mk, m, op.CommandID); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	goalOp, err := f.finishControlOp(ctx, mk, op)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	return after, []outbox.Op{op}, goalOp, nil
}

// applyNotDone re-arms a finished goal: the agent rewrites the goal file
// and types the harness's set-goal form under the same id (StartGoal with
// rearm_reason). VERIFYING moves to DISAGREE with a done_disagreement
// item; the goal returns to RUNNING when the goal line is WORKING again.
func (f *Fleet) applyNotDone(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	if m.GetText() == "" {
		return statedb.GoalRow{}, nil, nil, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "text is required for NOT_DONE", nil,
			[]*typev1.FieldViolation{{Field: "text", Description: "why it is not done is required"}})
	}
	op, err := f.enqueueControlCmd(ctx, mk, &agentv1.Command{
		Kind: &agentv1.Command_StartGoal{StartGoal: &agentv1.StartGoal{
			Spec:            controlAgentSpec(ctl, ctl.spec),
			ReusePane:       ctl.status.GetPane(),
			RearmReason:     m.GetText(),
			ReadyTimeout:    readyTimeout(f),
			GoalFileTimeout: goalFileTimeout(f),
		}},
	}, rearmAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	status := cloneGoalStatus(ctl.status)
	status.LastOperation = &typev1.CommandId{Value: op.CommandID}
	statusJSON, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode not-done status: %w", err)
	}
	to := ctl.row.Phase
	if ctl.status.GetPhase() == hubv1.GoalPhase_GOAL_PHASE_VERIFYING {
		to = "GOAL_PHASE_DISAGREE"
	}
	after, err := f.moveControlGoal(ctx, mk.tx, ctl, statedb.GoalTransition{
		To: to, Reason: "GOAL_PHASE_REASON_UNSPECIFIED", Status: string(statusJSON),
		Actor: mk.who.GetId().GetValue(), CommandID: op.CommandID,
		NewRunCause: "GOAL_RUN_CAUSE_REARM", NewRunMachine: ctl.row.MachineID,
		Fingerprint: goals.Fingerprint(status.GetLineState(), status.GetFirstLine(), false,
			status.GetHeadCommit(), to, m.GetText()),
		NowMs: mk.now.UnixMilli(),
	}, actorOf(mk.who), rearmAuditAction, mk.now.UnixMilli())
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.stageControlItem(ctx, mk.tx, ctl, after); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.ensureDisagreeTriage(ctx, mk.tx, ctl, after, mk.now.UnixMilli()); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.logControlRequest(ctx, mk, m, op.CommandID); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	goalOp, err := f.finishControlOp(ctx, mk, op)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	return after, []outbox.Op{op}, goalOp, nil
}
