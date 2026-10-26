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
const (
	controlAuditAction     = "goal.control"
	rearmAuditAction       = "goal.rearm"
	controlMoveAuditAction = "goal.state_change"
)

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
	mk := &controlCommandKit{
		tx: tx, who: who, machineID: machineID, pane: ctl.status.GetPane(),
		now: now, requestID: m.GetRequestId(), catalogHash: inv.catalogHash, control: inv.control,
	}
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
