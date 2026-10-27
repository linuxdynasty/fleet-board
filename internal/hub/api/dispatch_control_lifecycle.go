package api

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// applyPause sends the harness's pause (or C-c) and records who paused it.
// The phase does not move here: the agent's next GoalState names PAUSED.
func (f *Fleet) applyPause(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	var cmd *agentv1.Command
	switch mk.control.GetPauseMode() {
	case typev1.GoalPauseMode_GOAL_PAUSE_MODE_SUBCOMMAND:
		if !hasSubcommand(mk.control, typev1.GoalSubcommand_GOAL_SUBCOMMAND_PAUSE) {
			return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "/goal pause")
		}
		cmd = goalCommand(mk, "pause")
	case typev1.GoalPauseMode_GOAL_PAUSE_MODE_INTERRUPT:
		cmd = interruptCommand(mk)
	default:
		return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "pause")
	}
	op, err := f.enqueueControlCmd(ctx, mk, cmd, controlAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	status := cloneGoalStatus(ctl.status)
	status.PauseReason = hubv1.GoalPauseReason_GOAL_PAUSE_REASON_OPERATOR
	status.PausedBy = mk.who
	status.ReviewRoundsBaseline = status.GetReviewRounds()
	status.LastOperation = &typev1.CommandId{Value: op.CommandID}
	raw, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode pause status: %w", err)
	}
	after, err := f.moveControlGoal(ctx, mk.tx, ctl, statedb.GoalTransition{
		To: ctl.row.Phase, Reason: ctl.row.Reason, Status: string(raw),
		Actor: mk.who.GetId().GetValue(), CommandID: op.CommandID,
		Fingerprint: goals.Fingerprint(status.GetLineState(), status.GetFirstLine(), false,
			status.GetHeadCommit(), ctl.row.Phase, ""),
		NowMs: mk.now.UnixMilli(),
	}, actorOf(mk.who), controlAuditAction, mk.now.UnixMilli())
	if err != nil {
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

// applyResume sends the harness's resume (or restarts the run). Like a
// pause it moves no phase: the agent's next GoalState names the resume.
func (f *Fleet) applyResume(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	switch mk.control.GetResumeMode() {
	case typev1.GoalResumeMode_GOAL_RESUME_MODE_SUBCOMMAND:
		if !hasSubcommand(mk.control, typev1.GoalSubcommand_GOAL_SUBCOMMAND_RESUME) {
			return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "/goal resume")
		}
		return f.sendResumeCmd(ctx, mk, m, ctl, goalCommand(mk, "resume"))
	case typev1.GoalResumeMode_GOAL_RESUME_MODE_TYPE_TEXT:
		text := mk.control.GetResumeText()
		if text == "" {
			return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "typed resume")
		}
		return f.sendResumeCmd(ctx, mk, m, ctl, &agentv1.Command{
			Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{
				Pane: mk.pane,
				Keys: []*typev1.KeyInput{
					{Input: &typev1.KeyInput_Literal{Literal: text}},
					{Input: &typev1.KeyInput_Key{Key: typev1.NamedKey_NAMED_KEY_ENTER}},
				},
				Purpose: typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_GOAL_RESUME,
			}},
		})
	default:
		return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "resume")
	}
}

// sendResumeCmd sends one resume command and records it as the latest
// operation.
func (f *Fleet) sendResumeCmd(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal, cmd *agentv1.Command,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	op, err := f.enqueueControlCmd(ctx, mk, cmd, controlAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	status := cloneGoalStatus(ctl.status)
	status.LastOperation = &typev1.CommandId{Value: op.CommandID}
	raw, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode resume status: %w", err)
	}
	after, err := f.moveControlGoal(ctx, mk.tx, ctl, statedb.GoalTransition{
		To: ctl.row.Phase, Reason: ctl.row.Reason, Status: string(raw),
		Actor: mk.who.GetId().GetValue(), CommandID: op.CommandID,
		Fingerprint: goals.Fingerprint(status.GetLineState(), status.GetFirstLine(), false,
			status.GetHeadCommit(), ctl.row.Phase, ""),
		NowMs: mk.now.UnixMilli(),
	}, actorOf(mk.who), controlAuditAction, mk.now.UnixMilli())
	if err != nil {
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

// applyClear clears the harness goal and ends the goal CLEARED (or
// CANCELLED when it was paused because the provider closed the item). A
// harness mid-turn takes a precursor Interrupt in its own row first. With
// no pane (START_UNKNOWN) there is nothing to send: the move commits
// alone.
func (f *Fleet) applyClear(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest,
	ctl *controlGoal,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	to, reason := "GOAL_PHASE_CLEARED", "GOAL_PHASE_REASON_OPERATOR_CLEAR"
	if ctl.status.GetPhase() == hubv1.GoalPhase_GOAL_PHASE_PAUSED &&
		ctl.status.GetPauseReason() == hubv1.GoalPauseReason_GOAL_PAUSE_REASON_WORK_ITEM_CLOSED {
		to, reason = "GOAL_PHASE_CANCELLED", "GOAL_PHASE_REASON_WORK_ITEM_CLOSED"
	}
	var ops []outbox.Op
	if mk.pane != nil {
		if !hasSubcommand(mk.control, typev1.GoalSubcommand_GOAL_SUBCOMMAND_CLEAR) {
			return statedb.GoalRow{}, nil, nil, subcommandUnsupported(ctl.spec.GetHarness().GetName(), "/goal clear")
		}
		if ctl.status.GetPromptPending() || ctl.status.GetLineState() == typev1.GoalLineState_GOAL_LINE_STATE_WORKING {
			first, err := f.enqueueControlCmd(ctx, mk, interruptCommand(mk), controlAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
			if err != nil {
				return statedb.GoalRow{}, nil, nil, err
			}
			ops = append(ops, first)
		}
		op, err := f.enqueueControlCmd(ctx, mk, goalCommand(mk, "clear"), controlAuditAction, "goals/"+ctl.row.ID, ctl.row.ID)
		if err != nil {
			return statedb.GoalRow{}, nil, nil, err
		}
		ops = append(ops, op)
	}
	status := cloneGoalStatus(ctl.status)
	if len(ops) > 0 {
		status.LastOperation = &typev1.CommandId{Value: ops[len(ops)-1].CommandID}
	}
	raw, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, nil, fmt.Errorf("dispatch: encode clear status: %w", err)
	}
	after, err := f.moveControlGoal(ctx, mk.tx, ctl, statedb.GoalTransition{
		To: to, Reason: reason, Status: string(raw),
		Actor:     mk.who.GetId().GetValue(),
		CommandID: lastCommandOf(ops),
		Fingerprint: goals.Fingerprint(status.GetLineState(), status.GetFirstLine(), false,
			status.GetHeadCommit(), to, ""),
		NowMs: mk.now.UnixMilli(),
	}, actorOf(mk.who), controlAuditAction, mk.now.UnixMilli())
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.stageControlItem(ctx, mk.tx, ctl, after); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if err := f.logControlRequest(ctx, mk, m, lastCommandOf(ops)); err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	if len(ops) == 0 {
		return after, nil, nil, nil
	}
	goalOp, err := f.finishControlOp(ctx, mk, ops[len(ops)-1])
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	return after, ops, goalOp, nil
}
