package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// insertDispatchTx commits one manual dispatch: the STARTING goal with
// run 1 and transition 1, the work item to DISPATCHED, the StartGoal
// command in the outbox, and the goal.start audit. The attempt chains
// the work item's latest goal, so every retry is a new goal with
// attempt + 1 and previous_goal set (03h "Attempts").
func (f *Fleet) insertDispatchTx(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.StartGoalRequest, fx *dispatchFacts, goalID string, now time.Time,
) (statedb.GoalRow, *hubv1.Operation, outbox.Op, error) {
	in, requestID, skipPreflight := m.GetInput(), m.GetRequestId(), m.GetSkipPreflight()
	attempt, previous, err := statedb.LatestAttemptTx(ctx, tx, fx.itemKey)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: read attempts: %w", err)
	}
	def, status := dispatchGoalProto(in, fx, goalID, attempt+1, previous, who.GetId().GetValue(), now)
	defJSON, err := protojson.Marshal(def)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: encode goal definition: %w", err)
	}
	statusJSON, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: encode goal status: %w", err)
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_StartGoal{StartGoal: &agentv1.StartGoal{
			Spec:            fx.agentSpec(goalID),
			ReusePane:       in.GetPlacement().GetReusePane(),
			ReadyTimeout:    readyTimeout(f),
			GoalFileTimeout: goalFileTimeout(f),
			SkipPreflight:   skipPreflight,
		}},
	}
	// Goal work always happens in the fleet server (ADR 0011 decision 4):
	// the placement names the session, the hub names the server.
	if sess := in.GetPlacement().GetInSession(); sess != nil {
		cmd.GetStartGoal().InSession = &agentv1.SessionTarget{
			Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET,
			By:     &agentv1.SessionTarget_SessionId{SessionId: sess},
		}
	}
	if name := in.GetPlacement().GetWindowName(); name != "" {
		cmd.GetStartGoal().WindowName = name
	}
	cmd.RequestedBy = who
	cmd.IssuedTime = timestamppb.New(now)
	op, err := outbox.Enqueue(ctx, tx, outbox.In{
		MachineID: fx.machineID, Command: cmd, RequestID: requestID, GoalID: goalID, Now: now,
		Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
	})
	if err != nil {
		if errors.Is(err, outbox.ErrMachineUnknown) || errors.Is(err, outbox.ErrMachineRevoked) {
			return statedb.GoalRow{}, nil, outbox.Op{}, mapOutboxError(err)
		}
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: enqueue start: %w", err)
	}
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	if err := statedb.InsertDispatchedGoal(ctx, tx, statedb.DispatchedGoal{
		ID: goalID, MachineID: fx.machineID, Spec: string(defJSON), Status: string(statusJSON),
		Phase: "GOAL_PHASE_STARTING", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
		WorkItem: fx.itemKey, Repo: in.GetRepo().GetHost() + "/" + in.GetRepo().GetPath(), Branch: fx.branch,
		Attempt: attempt + 1, Version: 1, PreviousGoal: previous,
		RunCause: "GOAL_RUN_CAUSE_START", Actor: who.GetId().GetValue(),
		CommandID: op.CommandID, StartMs: now.UnixMilli(), NowMs: now.UnixMilli(), HubSeq: st.HubSeq,
	}); err != nil {
		if errors.Is(err, statedb.ErrWorkItemBusy) {
			return statedb.GoalRow{}, nil, outbox.Op{}, apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY,
				"the work item already has a live goal", nil, nil)
		}
		if errors.Is(err, statedb.ErrBranchBusy) {
			return statedb.GoalRow{}, nil, outbox.Op{}, apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_BRANCH_BUSY,
				"another live goal works on the branch", nil, nil)
		}
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: insert goal: %w", err)
	}
	stage, reason := goals.WorkStageAfter(hubv1.GoalPhase_GOAL_PHASE_STARTING)
	if err := statedb.SetWorkItemGoalStage(ctx, tx, fx.itemKey, int32(stage), int32(reason), st.HubSeq); err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("dispatch: stage work item: %w", err)
	}
	// The replay check hashes the whole request, so the log must too: a
	// GoalSpecInput hashes differently, and every identical retry would
	// read back as a conflict.
	if err := logRequest(ctx, tx, who.GetId().GetValue(), requestID, op.CommandID, dispatchHash(m), now); err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Actor: actorOf(who), Action: dispatchAuditAction, Target: "goals/" + goalID,
		CommandID: op.CommandID, RequestID: requestID, Now: now,
	}); err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	out, err := operationFromOp(ctx, tx, op, fx.machineID, who, requestID, now)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	after, err := statedb.ReadGoalTx(ctx, tx, goalID)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	return after, out, op, nil
}

// dispatchGoalProto assembles the GoalDefinition and the STARTING
// GoalStatus of one manual dispatch (02b: the definition is what was
// decided, the status what the hub concluded). The status carries the
// volatile goal-file fields unset: the agent reports them, the hub never
// invents them.
func dispatchGoalProto(in *hubv1.GoalSpecInput, fx *dispatchFacts, goalID string,
	attempt uint32, previous, actor string, now time.Time,
) (*hubv1.GoalDefinition, *hubv1.GoalStatus) {
	place := in.GetPlacement()
	def := &hubv1.GoalDefinition{
		WorkItem: &typev1.WorkItemRef{
			Key:     in.GetWorkItem(),
			Display: fx.display,
		},
		Repo:           &typev1.RepoRef{Host: in.GetRepo().GetHost(), Path: in.GetRepo().GetPath()},
		ChangeRequest:  fx.changeRequest,
		Machine:        &typev1.MachineId{Value: fx.machineID},
		Harness:        &typev1.Harness{Name: fx.harness},
		Profile:        place.GetProfile(),
		Account:        &typev1.AccountId{Value: fx.accountID},
		Model:          in.GetModel(),
		Effort:         in.GetEffort(),
		Consent:        fx.consent,
		GoalText:       in.GetGoalText(),
		DispatchReason: dispatchReasonOf(in, actor),
		FreshContext:   in.GetFreshContext(),
		Attempt:        attempt,
		PreviousGoal:   &typev1.GoalId{Value: previous},
		Origin:         hubv1.GoalOrigin_GOAL_ORIGIN_DISPATCHED,
	}
	status := &hubv1.GoalStatus{
		ObservedTime: timestamppb.New(now),
		Phase:        hubv1.GoalPhase_GOAL_PHASE_STARTING,
		PhaseTime:    timestamppb.New(now),
		PhaseReason:  hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED,
		Branch:       fx.branch,
		StartTime:    timestamppb.New(now),
	}
	if status.GetPhase() == hubv1.GoalPhase_GOAL_PHASE_STARTING {
		status.StartTime = timestamppb.New(now)
	}
	return def, status
}

// dispatchReasonOf prefixes the operator's reason, so the adapter types
// "manual dispatch by ..." into the goal text (03c planning rule 5).
func dispatchReasonOf(in *hubv1.GoalSpecInput, actor string) string {
	if r := in.GetDispatchReason(); r != "" {
		return "manual dispatch by " + actor + ": " + r
	}
	return "manual dispatch by " + actor
}

// raiseDispatchConfirm creates the DISPATCH_CONFIRM approval for a
// StartGoal the checks hold (02m "Actions that need an approval"): the
// response carries the approval and no operation, nothing is sent, and
// no goal exists until the owner approves. The input and the resolved
// machine persist beside the approval, so DecideApproval starts the same
// goal the checks ran on.
func (f *Fleet) raiseDispatchConfirm(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.StartGoalRequest, in *hubv1.GoalSpecInput, fx *dispatchFacts, now time.Time,
) (*hubv1.Approval, int64, error) {
	requestID := m.GetRequestId()
	timeout, err := approvalTimeoutTx(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	preview := "dispatch " + fx.display + " on " + fx.machineID + " (" + fx.harness + "): " + confirmReasons(fx.decision)
	created, seq, err := approvals.CreateApproval(ctx, tx, approvals.Create{
		Kind:          hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM,
		Severity:      hubv1.Severity_SEVERITY_WARNING,
		Preview:       truncatePreview(preview),
		MachineID:     fx.machineID,
		Allowed:       []approvals.Choice{{Key: "approve"}, {Key: "deny"}},
		DefaultChoice: "deny",
		Fallback:      hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
		ExpireTime:    now.Add(timeout),
		RequiredRole:  typev1.Role_ROLE_OWNER,
		Now:           now,
		Checkpoint:    func() error { return quotaCheckpoint(ctx, quotaWrite) },
	})
	if err != nil {
		return nil, 0, err
	}
	// The whole request persists (input, acks, skip flag), so the
	// approval starts exactly what its checks ran on.
	stored := &hubv1.StartGoalRequest{
		RequestId: requestID, Input: in,
		AcknowledgedWarnings: m.GetAcknowledgedWarnings(), SkipPreflight: m.GetSkipPreflight(),
	}
	inputJSON, err := protojson.Marshal(stored)
	if err != nil {
		return nil, 0, fmt.Errorf("dispatch: encode confirm input: %w", err)
	}
	// The reason sets pin what the approval was raised on: the decided
	// approve starts the goal only when no fresh CONFIRM or WARN falls
	// outside them.
	if err := statedb.InsertDispatchConfirm(ctx, tx, statedb.DispatchConfirm{
		ApprovalID: created.GetId().GetValue(), InputJSON: string(inputJSON),
		MachineID: fx.machineID, ConfirmReasons: checkReasons(fx.decision.Confirms),
		WarnReasons: checkReasons(fx.decision.Unacked), CreateMs: now.UnixMilli(),
	}); err != nil {
		return nil, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO request_log(principal_id, request_id, command_id, approval_id, request_hash, create_time)
		VALUES (?, ?, '', ?, ?, ?)`, who.GetId().GetValue(), requestID,
		created.GetId().GetValue(), dispatchHash(m), now.UnixMilli()); err != nil {
		return nil, 0, fmt.Errorf("dispatch: log confirm: %w", err)
	}
	// No audit row on raise: like every command gate, the audit lands
	// when the approval is decided, expires or is withdrawn (02m).
	return created, seq, nil
}

// confirmReasons names the CONFIRM checks for the approval preview.
func confirmReasons(d goals.Decision) string {
	var names []string
	for _, c := range d.Confirms {
		names = append(names, c.GetCheck().GetReason().String())
	}
	if len(names) == 0 {
		return "confirm"
	}
	out := names[0]
	for _, n := range names[1:] {
		out += ", " + n
	}
	if len(out) > 200 {
		out = out[:200]
	}
	return out
}

// checkDispatchReplay replays a repeated StartGoal request_id: the stored
// Operation and goal come back and nothing is sent twice (02b
// idempotency). A different body with the same key is REQUEST_ID_CONFLICT.
func (f *Fleet) checkDispatchReplay(ctx context.Context, tx *sql.Tx, principal string,
	m *hubv1.StartGoalRequest,
) (commandID, approvalID string, replay bool, err error) {
	var stored []byte
	err = tx.QueryRowContext(ctx, `SELECT command_id, approval_id, request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, principal, m.GetRequestId()).Scan(&commandID, &approvalID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("dispatch: replay check: %w", err)
	}
	if string(stored) != string(dispatchHash(m)) {
		return "", "", false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	return commandID, approvalID, true, nil
}

// dispatchCommandGoal reads the goal an outbox command dispatched.
func dispatchCommandGoal(ctx context.Context, tx *sql.Tx, commandID string) (string, error) {
	var goalID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT goal_id FROM commands WHERE command_id = ?`,
		commandID).Scan(&goalID); err != nil {
		return "", fmt.Errorf("dispatch: read command goal: %w", err)
	}
	return goalID.String, nil
}

// readWorkItemForPublish reads the staged work item row for the Watch
// publish.
func readWorkItemForPublish(ctx context.Context, tx *sql.Tx, key string) (statedb.WorkItemRow, error) {
	row, err := statedb.ReadWorkItemRowTx(ctx, tx, key)
	if err != nil {
		return statedb.WorkItemRow{}, fmt.Errorf("dispatch: read work item: %w", err)
	}
	return row, nil
}

// publishDispatchWorkItem publishes a goal move's work item stage as a
// WORK_ITEMS UPDATED change.
func (f *Fleet) publishDispatchWorkItem(ctx context.Context, after statedb.WorkItemRow) {
	f.PublishWorkItems(ctx, []statedb.WorkItemChange{{
		Key: after.Key, HubSeq: after.HubSeq, After: after,
	}})
}

// blockError renders the first BLOCK check as FAILED_PRECONDITION with
// every BLOCK in violations (02b, 03h). ErrorDetail carries no message,
// so the violation describes the reason and its metadata detail.
func blockError(d goals.Decision) error {
	violations := make([]*typev1.FieldViolation, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		violations = append(violations, &typev1.FieldViolation{
			Field: "input", Reason: b.GetCheck().GetReason(), Description: checkDescription(b),
		})
	}
	return apiError(connect.CodeFailedPrecondition, d.Block.GetCheck().GetReason(),
		"the dispatch is blocked", d.Block.GetCheck().GetMetadata(), violations)
}

// warnUnackedError renders unacknowledged WARN checks as
// PREFLIGHT_WARNINGS_UNACKNOWLEDGED (03h "the unacknowledged-warning
// rule").
func warnUnackedError(d goals.Decision) error {
	violations := make([]*typev1.FieldViolation, 0, len(d.Unacked))
	for _, w := range d.Unacked {
		violations = append(violations, &typev1.FieldViolation{
			Field: "input.acknowledged_warnings", Reason: w.GetCheck().GetReason(),
			Description: checkDescription(w),
		})
	}
	return apiError(connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_PREFLIGHT_WARNINGS_UNACKNOWLEDGED,
		"acknowledge the warnings to dispatch past them", nil, violations)
}

// checkDescription names a failed check for a violation: its reason and
// the detail metadata the check carries.
func checkDescription(c *hubv1.PreflightCheck) string {
	if d := c.GetCheck().GetMetadata()["detail"]; d != "" {
		return c.GetCheck().GetReason().String() + ": " + d
	}
	return c.GetCheck().GetReason().String()
}

// dispatchHash is the idempotency hash of a dispatch request body, so
// different acks or flags are different requests.
func dispatchHash(msg proto.Message) []byte {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(body)
	return sum[:]
}

// startDecidedDispatch lives in dispatch_decide.go: it starts the goal a
// DISPATCH_CONFIRM approval held, in the Decide transaction.

// dispatchRefused logs a dispatch caller error by its reason and returns
// it.
func (f *Fleet) dispatchRefused(ctx context.Context, err error) error {
	f.countDispatch(ctx, "dispatch", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err))
	return err
}

// dispatchRefused logs a dispatch caller error with the actor.
func (f *Fleet) dispatchRefusedAs(ctx context.Context, actor string, err error) error {
	f.countDispatch(ctx, "dispatch", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err), "actor", actor)
	return err
}

// dispatchStoreError renders a writer-job failure: caller errors stay as
// refused, the hub's own become INTERNAL.
func (f *Fleet) dispatchStoreError(ctx context.Context, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.dispatchRefusedAs(ctx, actor, err)
	}
	if errors.Is(err, statedb.ErrWorkItemBusy) {
		return f.dispatchRefusedAs(ctx, actor, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY,
			"the work item already has a live goal", nil, nil))
	}
	if errors.Is(err, statedb.ErrBranchBusy) {
		return f.dispatchRefusedAs(ctx, actor, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_BRANCH_BUSY,
			"another live goal works on the branch", nil, nil))
	}
	f.countDispatch(ctx, "dispatch", "failed")
	kind := "io"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "dispatch.failed", "step", "store", "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not dispatch the goal", nil, nil)
}

// dispatchResult names the hub-check outcome for the preflight counter.
func dispatchResult(d goals.Decision) string {
	switch d.Outcome {
	case goals.StartBlocked:
		return "blocked"
	case goals.StartConfirm:
		return "confirm"
	case goals.StartWarnUnacked:
		return "warn_unacked"
	default:
		return "ok"
	}
}

// dispatchResultOf names the StartGoal outcome for the dispatch counter.
func dispatchResultOf(resp *hubv1.StartGoalResponse) string {
	if resp == nil {
		return "failed"
	}
	if resp.GetApproval() != nil {
		return "confirm"
	}
	return "sent"
}

// respGoalWorkItem names the work item a StartGoal response dispatched.
func respGoalWorkItem(resp *hubv1.StartGoalResponse, in *hubv1.GoalSpecInput) string {
	if resp.GetGoal() == nil {
		return ""
	}
	return workItemKeyOf(in)
}
