package api

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// requeueAuditAction is the audit action of a RequeueWorkItem call (02m).
const requeueAuditAction = "work_item.requeue"

// RequeueWorkItem puts a MANUAL_ONLY work item back to READY, so the
// scheduler may take it again after a person stopped it or it failed to
// start (03h "Requeue, as messages"). A requeue starts nothing: the next
// scheduler pass (or a manual dispatch) starts the work. The trusted
// gate holds: an UNTRUSTED or EDITED_AFTER_TRUST item is refused, and a
// stale expected_version fails the read again.
func (f *Fleet) RequeueWorkItem(ctx context.Context, req *connect.Request[hubv1.RequeueWorkItemRequest],
) (*connect.Response[hubv1.RequeueWorkItemResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.requeueRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.requeueRefusedAs(ctx, actor, err)
	}
	if m.GetId() == nil {
		return nil, f.requeueRefusedAs(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "work item id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "a work item id is required"}}))
	}
	key := workItemKeyOf(&hubv1.GoalSpecInput{WorkItem: m.GetId()})
	now := f.hubNow()
	var after statedb.WorkItemRow
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		if replay, err := f.checkRequeueReplay(ctx, tx, actor, m); err != nil || replay {
			if err != nil {
				return err
			}
			// A committed retry returns the stored item: no mutation,
			// no new audit row.
			after, err = readWorkItemForPublish(ctx, tx, key)
			return err
		}
		st, err := statedb.GetWorkItemStateTx(ctx, tx, key)
		if err != nil {
			return err
		}
		switch scheduler.CheckRequeue(hubv1.WorkStage(st.Stage), hubv1.WorkManualReason(st.ManualReason),
			uint64(st.Version), m.GetExpectedVersion()) { //nolint:gosec // G115: versions start at 1 and only rise
		case scheduler.RequeueOK:
			// Proceeds below.
		case scheduler.RequeueVersionMismatch:
			return apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
				"the work item changed since it was read: read again", nil, nil)
		default:
			return apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_REQUEUE_NOT_ALLOWED,
				"only a trusted MANUAL_ONLY item requeues: dispatch a READY one, or clear the gate first", nil, nil)
		}
		stamp, err := statedb.NextStamp(ctx, tx, now)
		if err != nil {
			return err
		}
		if err := statedb.SetWorkItemGoalStage(ctx, tx, key,
			int32(hubv1.WorkStage_WORK_STAGE_READY),
			int32(hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED), stamp.HubSeq); err != nil {
			return err
		}
		if err := logRequest(ctx, tx, actor, m.GetRequestId(), "", dispatchHash(m), now); err != nil {
			return err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: requeueAuditAction, Target: "work_items/" + key,
			RequestID: m.GetRequestId(), Now: now,
		}); err != nil {
			return err
		}
		after, err = readWorkItemForPublish(ctx, tx, key)
		return err
	}, &dispAudits), func() {
		f.publishDispatchWorkItem(ctx, after)
		f.publishAudits(dispAudits)
	})
	if submitErr != nil {
		return nil, f.requeueStoreError(ctx, actor, submitErr)
	}
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.requeueStoreError(ctx, actor, commitErr)
	}
	v, err := workItemViewOf(after)
	if err != nil {
		f.countDispatch(ctx, "requeue", "failed")
		return nil, apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not read the requeued item", nil, nil)
	}
	w, err := workItemProto(v)
	if err != nil {
		f.countDispatch(ctx, "requeue", "failed")
		return nil, apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not read the requeued item", nil, nil)
	}
	f.countDispatch(ctx, "requeue", "requeued")
	f.tel.Logger.InfoContext(ctx, "dispatch.planned", "actor", actor,
		"work_item", key, "mode", "requeue")
	return connect.NewResponse(&hubv1.RequeueWorkItemResponse{WorkItem: w}), nil
}

// checkRequeueReplay replays a repeated RequeueWorkItem request_id: the
// stored item comes back and nothing mutates twice (02b idempotency). A
// different body with the same key is REQUEST_ID_CONFLICT.
func (f *Fleet) checkRequeueReplay(ctx context.Context, tx *sql.Tx, principal string,
	m *hubv1.RequeueWorkItemRequest,
) (bool, error) {
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, principal, m.GetRequestId()).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(stored) != string(dispatchHash(m)) {
		return false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	return true, nil
}

// isRequeueReplayConflict reports a lost logRequest INSERT race: a
// concurrent duplicate committed first. The single writer serializes
// transactions, so the replay check above already sees the winner's row;
// this mapping is the backstop, and it never surfaces as INTERNAL.
func isRequeueReplayConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: request_log")
}

// requeueRefused logs a requeue caller error and returns it.
func (f *Fleet) requeueRefused(ctx context.Context, err error) error {
	f.countDispatch(ctx, "requeue", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err))
	return err
}

// requeueRefused logs a requeue caller error with the actor.
func (f *Fleet) requeueRefusedAs(ctx context.Context, actor string, err error) error {
	f.countDispatch(ctx, "requeue", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err), "actor", actor)
	return err
}

// requeueStoreError renders a writer-job failure: caller errors stay as
// refused (including an unknown item), the hub's own become INTERNAL.
func (f *Fleet) requeueStoreError(ctx context.Context, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.requeueRefusedAs(ctx, actor, err)
	}
	if errors.Is(err, statedb.ErrWorkItemUnknown) {
		return f.requeueRefusedAs(ctx, actor, apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown work item", nil, nil))
	}
	if isRequeueReplayConflict(err) {
		return f.requeueRefusedAs(ctx, actor, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was already used: retry with a fresh one", nil, nil))
	}
	f.countDispatch(ctx, "requeue", "failed")
	kind := "io"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "dispatch.failed", "step", "store", "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not requeue the item", nil, nil)
}
