package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// sendDecided enqueues what an approved decision releases, in the Decide
// transaction: the typed answer for a prompt (bound to its prompt_id), the
// stored command for a confirm, and for a dispatch confirm the goal its
// checks ran on (02m "row 22's StartGoal reads the decided approval and
// starts the goal"). It reports the started goal and its work item beside
// the operation, so the Decide hook publishes them.
func (f *Fleet) sendDecided(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	outcome approvals.Outcome, requestID string, now time.Time,
) (sent *hubv1.Operation, sentKind string, goal statedb.GoalRow, item statedb.WorkItemRow, started bool, err error) {
	a := outcome.Approval
	if a.GetKind() == hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM {
		return f.sendDecidedDispatch(ctx, tx, who, a, requestID, now)
	}
	cmd, err := commandForDecision(ctx, tx, a, a.GetDecision(), outcome.PromptID)
	if err != nil || cmd == nil {
		return nil, "", statedb.GoalRow{}, statedb.WorkItemRow{}, false, err
	}
	sent, sentKind, err = f.sendApprovedTx(ctx, tx, who, a, cmd, requestID, now)
	return sent, sentKind, statedb.GoalRow{}, statedb.WorkItemRow{}, false, err
}

// sendDecidedDispatch runs the DISPATCH_CONFIRM branch of sendDecided:
// it starts the decided goal, converts the outbox entry to a Watch
// operation and counts the send, in the Decide transaction.
func (f *Fleet) sendDecidedDispatch(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	a *hubv1.Approval, requestID string, now time.Time,
) (*hubv1.Operation, string, statedb.GoalRow, statedb.WorkItemRow, bool, error) {
	op, goalAfter, itemAfter, err := f.startDecidedDispatch(ctx, tx, who, a, requestID, now)
	if err != nil {
		return nil, "", statedb.GoalRow{}, statedb.WorkItemRow{}, false, err
	}
	queued, err := operationFromOp(ctx, tx, op, a.GetMachine().GetValue(), who, requestID, now)
	if err != nil {
		return nil, "", statedb.GoalRow{}, statedb.WorkItemRow{}, false, err
	}
	if f.onCommandQueued != nil {
		f.onCommandQueued(op)
	}
	f.countSession(ctx, "start_goal", "sent")
	return queued, "start_goal", goalAfter, itemAfter, true, nil
}

// startDecidedDispatch starts the goal a DISPATCH_CONFIRM approval held
// (02m "row 22's StartGoal reads the decided approval and starts the
// goal"): the stored request runs its hub checks again on the pinned
// machine. A BLOCK stops the approve, and so does any fresh CONFIRM or
// WARN outside the sets the approval was raised on: unseen risk must not
// start a goal. Races on the work item and branch fail the insert and
// roll the decision back with them.
func (f *Fleet) startDecidedDispatch(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	approval *hubv1.Approval, decideRequestID string, now time.Time,
) (outbox.Op, statedb.GoalRow, statedb.WorkItemRow, error) {
	confirm, err := statedb.ReadDispatchConfirmTx(ctx, tx, approval.GetId().GetValue())
	if err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{},
			fmt.Errorf("dispatch: read decided input: %w", err)
	}
	var stored hubv1.StartGoalRequest
	if err := protojson.Unmarshal([]byte(confirm.InputJSON), &stored); err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{},
			fmt.Errorf("dispatch: read decided input: %w", err)
	}
	in, ok := proto.Clone(stored.GetInput()).(*hubv1.GoalSpecInput)
	if !ok || in == nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{},
			fmt.Errorf("dispatch: decided input is not a goal input")
	}
	if in.GetPlacement() == nil {
		in.Placement = &hubv1.GoalPlacement{}
	}
	in.GetPlacement().Machine = &typev1.MachineId{Value: confirm.MachineID}
	facts, err := f.gatherDispatchFacts(ctx, tx, who, in, stored.GetAcknowledgedWarnings(), now)
	if err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, err
	}
	if facts.decision.Outcome == goals.StartBlocked {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, blockError(facts.decision)
	}
	if fresh := freshChecks(facts.decision, confirm.ConfirmReasons, confirm.WarnReasons); len(fresh) > 0 {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, changedChecksError(fresh)
	}
	goalID, err := ulid.New(now, f.random)
	if err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{},
			fmt.Errorf("dispatch: mint decided goal id: %w", err)
	}
	req := &hubv1.StartGoalRequest{
		RequestId: decideRequestID, Input: in,
		AcknowledgedWarnings: stored.GetAcknowledgedWarnings(), SkipPreflight: stored.GetSkipPreflight(),
	}
	after, _, op, err := f.insertDispatchTx(ctx, tx, who, req, facts, goalID, now)
	if err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, err
	}
	if err := approvals.AttachOperation(ctx, tx, approval.GetId().GetValue(), op.CommandID, now); err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, err
	}
	if err := approvals.AttachGoal(ctx, tx, approval.GetId().GetValue(), goalID, now); err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, err
	}
	item, err := readWorkItemForPublish(ctx, tx, facts.itemKey)
	if err != nil {
		return outbox.Op{}, statedb.GoalRow{}, statedb.WorkItemRow{}, err
	}
	f.countDispatch(ctx, "dispatch_approved", "sent")
	f.tel.Logger.InfoContext(ctx, "dispatch.planned", "actor", who.GetId().GetValue(),
		"goal", goalID, "work_item", facts.itemKey, "machine", facts.machineID, "mode", "approved")
	return op, after, item, nil
}

// changedChecksError refuses a decided dispatch whose checks no longer
// match the approval: every fresh CONFIRM or WARN is a violation, so the
// owner sees exactly which risk they never accepted.
func changedChecksError(fresh []*hubv1.PreflightCheck) error {
	violations := make([]*typev1.FieldViolation, 0, len(fresh))
	for _, c := range fresh {
		violations = append(violations, &typev1.FieldViolation{
			Field: "input", Reason: c.GetCheck().GetReason(), Description: checkDescription(c),
		})
	}
	return apiError(connect.CodeFailedPrecondition, fresh[0].GetCheck().GetReason(),
		"the dispatch checks changed while the approval waited", fresh[0].GetCheck().GetMetadata(), violations)
}

// checkReasons names the reasons of one check list, for pinning what an
// approval was raised on.
func checkReasons(cs []*hubv1.PreflightCheck) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.GetCheck().GetReason().String())
	}
	return out
}

// freshChecks collects the decision's CONFIRM and unacked WARN checks
// outside the sets the approval was raised on.
func freshChecks(d goals.Decision, confirms, warns []string) []*hubv1.PreflightCheck {
	seen := map[string]bool{}
	for _, r := range confirms {
		seen["confirm\x00"+r] = true
	}
	for _, r := range warns {
		seen["warn\x00"+r] = true
	}
	var fresh []*hubv1.PreflightCheck
	for _, c := range d.Confirms {
		if !seen["confirm\x00"+c.GetCheck().GetReason().String()] {
			fresh = append(fresh, c)
		}
	}
	for _, c := range d.Unacked {
		if !seen["warn\x00"+c.GetCheck().GetReason().String()] {
			fresh = append(fresh, c)
		}
	}
	return fresh
}
