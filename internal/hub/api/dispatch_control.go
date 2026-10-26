package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// controlAuditAction is the audit of a ControlGoal that sends a command;
// re-arms audit as goal.rearm, hub-only moves as goal.state_change (02m).
const controlAuditAction = "goal.control"
const rearmAuditAction = "goal.rearm"
const controlMoveAuditAction = "goal.state_change"

// ControlGoal runs an operator's intent on a dispatched goal (02m
// "Control", 03l "Control actions by phase"). It guards on
// expected_phase (a different phase fails FAILED_PRECONDITION with
// GOAL_PHASE_CHANGED) and on the action table (anything else fails
// FAILED_PRECONDITION with GOAL_ACTION_NOT_ALLOWED). Harness actions
// enqueue one command and audit goal.control; re-arms audit goal.rearm;
// hub-only moves (MARK_NOT_STARTED, RECHECK) commit with no operation.
// The hub never invents phase moves the agent owns: a pause moves nothing
// (the agent's next GoalState names PAUSED), while re-arms open the next
// run beside the command. Typing detection (human_at_keyboard_until_time)
// is not tracked hub-side in row 22, so nothing is ever held.
func (f *Fleet) ControlGoal(ctx context.Context, req *connect.Request[hubv1.ControlGoalRequest],
) (*connect.Response[hubv1.ControlGoalResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.dispatchRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.dispatchRefusedAs(ctx, actor, err)
	}
	if m.GetGoal().GetValue() == "" {
		return nil, f.dispatchRefusedAs(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "goal is required", nil, nil))
	}
	if m.GetAction() == hubv1.GoalAction_GOAL_ACTION_UNSPECIFIED {
		return nil, f.dispatchRefusedAs(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "action is required", nil, nil))
	}
	if (m.GetAction() == hubv1.GoalAction_GOAL_ACTION_EDIT ||
		m.GetAction() == hubv1.GoalAction_GOAL_ACTION_NOT_DONE) && len(m.GetText()) > 4096 {
		return nil, f.dispatchRefusedAs(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "text is over 4 KiB", nil,
			[]*typev1.FieldViolation{{Field: "text", Description: "at most 4 KiB"}}))
	}
	now := f.hubNow()
	var resp *hubv1.ControlGoalResponse
	var queued outbox.Op
	var goalAfter statedb.GoalRow
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		if op, stored, replay, err := f.checkControlReplay(ctx, tx, actor, m); err != nil || replay {
			if err != nil {
				return err
			}
			if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
				return err
			}
			resp = &hubv1.ControlGoalResponse{Goal: stored, Operation: op}
			return nil
		}
		ctl, err := f.loadControlGoal(ctx, tx, who, m)
		if err != nil {
			return err
		}
		machineID := ctl.row.MachineID
		switch m.GetAction() {
		case hubv1.GoalAction_GOAL_ACTION_MARK_NOT_STARTED, hubv1.GoalAction_GOAL_ACTION_RECHECK:
			after, err := f.applyHubControl(ctx, tx, who, m, ctl, now)
			if err != nil {
				return err
			}
			goalAfter = after
			resp = &hubv1.ControlGoalResponse{Goal: mustGoalProtoOfRow(after)}
		default:
			after, ops, goalOp, err := f.applyHarnessControl(ctx, tx, who, m, ctl, machineID, now)
			if err != nil {
				return err
			}
			goalAfter = after
			if len(ops) > 0 {
				queued = ops[len(ops)-1]
			}
			resp = &hubv1.ControlGoalResponse{Goal: mustGoalProtoOfRow(after), Operation: goalOp}
		}
		return nil
	}, &dispAudits), func() {
		if queued.Created {
			f.publishOperationWatch(queued.HubSeq, resp.GetOperation())
			f.PublishGoalsCommitted(ctx, goalAfter.MachineID, nil, []statedb.GoalRow{goalAfter})
		} else if resp.GetGoal() != nil && resp.GetOperation() == nil {
			f.PublishGoalsCommitted(ctx, goalAfter.MachineID, nil, []statedb.GoalRow{goalAfter})
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
	f.countDispatch(ctx, "control", actionResult(m.GetAction()))
	f.tel.Logger.InfoContext(ctx, "dispatch.control", "actor", actor,
		"goal", m.GetGoal().GetValue(), "action", m.GetAction().String())
	return connect.NewResponse(resp), nil
}

// controlGoal is a ControlGoal's loaded target: the row and the parsed
// spec and status.
type controlGoal struct {
	row    statedb.GoalRow
	spec   *hubv1.GoalDefinition
	status *hubv1.GoalStatus
}

// checkControlReplay replays a repeated ControlGoal request_id: the
// stored Operation (nil for a hub-only move) and goal come back and
// nothing is sent twice.
func (f *Fleet) checkControlReplay(ctx context.Context, tx *sql.Tx, actor string,
	m *hubv1.ControlGoalRequest,
) (*hubv1.Operation, *hubv1.Goal, bool, error) {
	var commandID string
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT command_id, request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, actor, m.GetRequestId()).Scan(&commandID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("dispatch: control replay check: %w", err)
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, nil, false, fmt.Errorf("dispatch: control replay hash: %w", err)
	}
	sum := sha256.Sum256(body)
	if string(stored) != string(sum[:]) {
		return nil, nil, false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	if commandID == "" {
		// A hub-only move records no command: replay reads the goal back.
		row, err := statedb.ReadGoalTx(ctx, tx, m.GetGoal().GetValue())
		if err != nil {
			return nil, nil, false, fmt.Errorf("dispatch: control replay read: %w", err)
		}
		return nil, mustGoalProtoOfRow(row), true, nil
	}
	op, err := loadOperation(ctx, tx, commandID)
	if err != nil {
		return nil, nil, false, err
	}
	goalID, err := dispatchCommandGoal(ctx, tx, commandID)
	if err != nil {
		return nil, nil, false, err
	}
	row, err := statedb.ReadGoalTx(ctx, tx, goalID)
	if err != nil {
		return nil, nil, false, err
	}
	return op, mustGoalProtoOfRow(row), true, nil
}

// loadControlGoal reads the goal and guards it: observed goals are never
// controlled (GOAL_ACTION_NOT_ALLOWED with {"why": "observed"}, 02h), a
// stale expected_phase is GOAL_PHASE_CHANGED, anything the phase table
// refuses is GOAL_ACTION_NOT_ALLOWED.
func (f *Fleet) loadControlGoal(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.ControlGoalRequest,
) (*controlGoal, error) {
	row, err := statedb.ReadGoalTx(ctx, tx, m.GetGoal().GetValue())
	if err != nil {
		if errors.Is(err, statedb.ErrGoalUnknown) {
			return nil, apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
				"unknown goal", nil, nil)
		}
		return nil, fmt.Errorf("dispatch: read goal: %w", err)
	}
	var spec hubv1.GoalDefinition
	if err := protojson.Unmarshal([]byte(row.Spec), &spec); err != nil {
		return nil, fmt.Errorf("dispatch: read goal definition: %w", err)
	}
	var status hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(row.Status), &status); err != nil {
		return nil, fmt.Errorf("dispatch: read goal status: %w", err)
	}
	if spec.GetOrigin() != hubv1.GoalOrigin_GOAL_ORIGIN_DISPATCHED {
		return nil, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_GOAL_ACTION_NOT_ALLOWED,
			"observed goals are not controlled", map[string]string{"why": "observed"}, nil)
	}
	if want := m.GetExpectedPhase(); want != hubv1.GoalPhase_GOAL_PHASE_UNSPECIFIED &&
		want != status.GetPhase() {
		return nil, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_GOAL_PHASE_CHANGED,
			"the goal moved; read it again", map[string]string{"current_phase": status.GetPhase().String()}, nil)
	}
	if !goals.Allowed(m.GetAction(), status.GetPhase()) {
		return nil, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_GOAL_ACTION_NOT_ALLOWED,
			"the phase does not allow this action", map[string]string{"phase": status.GetPhase().String()}, nil)
	}
	return &controlGoal{row: row, spec: &spec, status: &status}, nil
}

// mustGoalProtoOfRow assembles a moved row: the row came from the
// writer's own transaction, so a decode failure is the hub's bug.
func mustGoalProtoOfRow(row statedb.GoalRow) *hubv1.Goal {
	g, err := goalProtoOfRow(row)
	if err != nil {
		panic(fmt.Sprintf("dispatch: moved goal does not decode: %v", err))
	}
	return g
}

// actionResult names a control action for the dispatch counter.
func actionResult(a hubv1.GoalAction) string {
	switch a {
	case hubv1.GoalAction_GOAL_ACTION_PAUSE:
		return "pause"
	case hubv1.GoalAction_GOAL_ACTION_RESUME:
		return "resume"
	case hubv1.GoalAction_GOAL_ACTION_EDIT:
		return "edit"
	case hubv1.GoalAction_GOAL_ACTION_CLEAR:
		return "clear"
	case hubv1.GoalAction_GOAL_ACTION_RECHECK:
		return "recheck"
	case hubv1.GoalAction_GOAL_ACTION_NOT_DONE:
		return "not_done"
	default:
		return "mark_not_started"
	}
}

// applyHarnessControl maps one harness action to agent commands and sends
// them, with the phase moves the hub owns (pause metadata, re-arm runs,
// CLEAR's end). It returns the moved row, every enqueued outbox row, and
// the response Operation (nil when no pane needed no command). Support is
// read from the machine's reported inventory, never a hub-kept name
// table; a mode the harness does not offer fails
// GOAL_SUBCOMMAND_UNSUPPORTED before anything is sent.
func (f *Fleet) applyHarnessControl(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.ControlGoalRequest, ctl *controlGoal, machineID string, now time.Time,
) (statedb.GoalRow, []outbox.Op, *hubv1.Operation, error) {
	inv, err := controlInventoryFor(ctx, tx, machineID, ctl.spec.GetHarness().GetName())
	if err != nil {
		return statedb.GoalRow{}, nil, nil, err
	}
	mk := &controlCommandKit{tx: tx, who: who, machineID: machineID, pane: ctl.status.GetPane(),
		now: now, requestID: m.GetRequestId(), catalogHash: inv.catalogHash, control: inv.control}
	switch m.GetAction() {
	case hubv1.GoalAction_GOAL_ACTION_PAUSE:
		return f.applyPause(ctx, mk, m, ctl)
	case hubv1.GoalAction_GOAL_ACTION_RESUME:
		return f.applyResume(ctx, mk, m, ctl)
	case hubv1.GoalAction_GOAL_ACTION_CLEAR:
		return f.applyClear(ctx, mk, m, ctl)
	case hubv1.GoalAction_GOAL_ACTION_EDIT:
		return f.applyEdit(ctx, mk, m, ctl)
	default: // GOAL_ACTION_NOT_DONE (Allowed refused everything else).
		return f.applyNotDone(ctx, mk, m, ctl)
	}
}

// controlCommandKit carries one control's command context: the pane every
// command targets, the request it answers, and the harness's reported
// support.
type controlCommandKit struct {
	tx          *sql.Tx
	who         *typev1.Principal
	machineID   string
	pane        *typev1.PaneKey
	now         time.Time
	requestID   string
	catalogHash string
	control     *typev1.GoalControl
}

// enqueue sends one agent command for a control, with its own command_id
// and the audit row the action names.
func (f *Fleet) enqueueControlCmd(ctx context.Context, mk *controlCommandKit, cmd *agentv1.Command,
	auditAction, target string,
) (outbox.Op, error) {
	cmd.RequestedBy = mk.who
	cmd.IssuedTime = timestamppb.New(mk.now)
	op, err := outbox.Enqueue(ctx, mk.tx, outbox.In{
		MachineID: mk.machineID, Command: cmd, Now: mk.now,
		Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
	})
	if err != nil {
		return outbox.Op{}, mapOutboxError(err)
	}
	if _, err := statedb.AppendAudit(ctx, mk.tx, statedb.AuditEntry{
		Actor: actorOf(mk.who), Action: auditAction, Target: target,
		CommandID: op.CommandID, RequestID: mk.requestID, Now: mk.now,
	}); err != nil {
		return outbox.Op{}, err
	}
	return op, nil
}

// finishControlOp builds the response Operation for the last command.
func (f *Fleet) finishControlOp(ctx context.Context, mk *controlCommandKit, op outbox.Op) (*hubv1.Operation, error) {
	return operationFromOp(ctx, mk.tx, op, mk.machineID, mk.who, mk.requestID, mk.now)
}

// hasSubcommand reports whether the harness lists a /goal subcommand.
func hasSubcommand(gc *typev1.GoalControl, sub typev1.GoalSubcommand) bool {
	for _, s := range gc.GetSubcommands() {
		if s == sub {
			return true
		}
	}
	return false
}

// goalCommand builds a /goal RunHarnessCommand the agent's catalog check
// accepts: the snapshot-reported hash, the GOAL class.
func goalCommand(mk *controlCommandKit, sub string) *agentv1.Command {
	return &agentv1.Command{
		Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{
			Pane: mk.pane, Command: "/goal", Args: sub,
			ExpectedClass: typev1.CommandClass_COMMAND_CLASS_GOAL, CatalogHash: mk.catalogHash,
		}},
	}
}

// interruptCommand builds the C-c the hub sends before a clear or an
// interrupt-mode pause.
func interruptCommand(mk *controlCommandKit) *agentv1.Command {
	return &agentv1.Command{
		Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: mk.pane}},
	}
}

// subcommandUnsupported refuses a mode the harness does not offer.
func subcommandUnsupported(harness, what string) error {
	return apiError(connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_GOAL_SUBCOMMAND_UNSUPPORTED,
		"the harness does not offer "+what, map[string]string{"harness": harness}, nil)
}

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
	op, err := f.enqueueControlCmd(ctx, mk, cmd, controlAuditAction, "goals/"+ctl.row.ID)
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
	op, err := f.enqueueControlCmd(ctx, mk, cmd, controlAuditAction, "goals/"+ctl.row.ID)
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
			first, err := f.enqueueControlCmd(ctx, mk, interruptCommand(mk), controlAuditAction, "goals/"+ctl.row.ID)
			if err != nil {
				return statedb.GoalRow{}, nil, nil, err
			}
			ops = append(ops, first)
		}
		op, err := f.enqueueControlCmd(ctx, mk, goalCommand(mk, "clear"), controlAuditAction, "goals/"+ctl.row.ID)
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

// lastCommandOf names the latest command for the audit and replay rows.
func lastCommandOf(ops []outbox.Op) string {
	if len(ops) == 0 {
		return ""
	}
	return ops[len(ops)-1].CommandID
}

// logControlRequest records the control's idempotency row: the last
// command, or none for a hub-only move.
func (f *Fleet) logControlRequest(ctx context.Context, mk *controlCommandKit, m *hubv1.ControlGoalRequest, commandID string) error {
	return logRequest(ctx, mk.tx, mk.who.GetId().GetValue(), mk.requestID, commandID, controlHash(m), mk.now)
}

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
	}, rearmAuditAction, "goals/"+ctl.row.ID)
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
	}, rearmAuditAction, "goals/"+ctl.row.ID)
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

// controlAgentSpec builds the agent's GoalSpec from the stored
// definition for a re-arm.
func controlAgentSpec(ctl *controlGoal, def *hubv1.GoalDefinition) *agentv1.GoalSpec {
	return &agentv1.GoalSpec{
		GoalId:    &typev1.GoalId{Value: ctl.row.ID},
		Workdir:   &typev1.Workdir{Repo: def.GetRepo()},
		Harness:   def.GetHarness(),
		AccountId: def.GetAccount(),
		Consent:   def.GetConsent(),
		GoalText:  def.GetGoalText(),
		Branch:    ctl.status.GetBranch(),
		WorkItem:  &typev1.WorkItemRef{Key: def.GetWorkItem().GetKey(), Display: def.GetWorkItem().GetDisplay()},
		Repo:      def.GetRepo(),
		Model:     def.GetModel(),
		Effort:    def.GetEffort(),
	}
}

// cloneGoalStatus copies a stored status for hub edits (pause metadata,
// last operation): the caller's row stays untouched.
func cloneGoalStatus(s *hubv1.GoalStatus) *hubv1.GoalStatus {
	out, ok := proto.Clone(s).(*hubv1.GoalStatus)
	if !ok {
		panic(fmt.Sprintf("dispatch: status does not clone: %T", s))
	}
	return out
}

// cloneGoalDef copies a stored definition for an EDIT.
func cloneGoalDef(d *hubv1.GoalDefinition) *hubv1.GoalDefinition {
	out, ok := proto.Clone(d).(*hubv1.GoalDefinition)
	if !ok {
		panic(fmt.Sprintf("dispatch: definition does not clone: %T", d))
	}
	return out
}
