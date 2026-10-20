package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// authorizeApprovals admits the caller for ListApprovals, GetApproval and
// DecideApproval (02d: READ|APPROVALS for the reads, APPROVALS to decide).
// The checked admin user and admitted local TCP owner cover every required
// role. Other USER principals need their own matching role.
func authorizeApprovals(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "APPROVALS")
}

// ListApprovals lists approvals newest first. Filter and order_by follow
// 02c once row 12 lands that query language; until then only the empty
// filter reads everything. Past-due rows expire in the same job, so a
// client polling the list sees EXPIRED like a Get would.
func (f *Fleet) ListApprovals(ctx context.Context, req *connect.Request[hubv1.ListApprovalsRequest],
) (*connect.Response[hubv1.ListApprovalsResponse], error) {
	if _, err := authorizeApprovals(ctx); err != nil {
		return nil, f.apprRefused(ctx, err)
	}
	m := req.Msg
	if m.GetFilter() != "" {
		return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "filters arrive with row 12", nil, nil))
	}
	if m.GetOrderBy() != "" {
		return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, "ordering arrives with row 12", nil, nil))
	}
	size := m.GetPageSize()
	if size <= 0 {
		size = 50
	}
	now := f.hubNow()
	var list []*hubv1.Approval
	var next string
	err := f.store.Write(ctx, func(tx *sql.Tx) error {
		filter := approvals.Filter{PageSize: int(size), PageToken: m.GetPageToken()}
		// Validate the cursor before quota admission without reading rows.
		// ExpireDue below is the first effect in this writer job.
		if err := approvals.ValidateListInput(filter, now); err != nil {
			return err
		}
		if err := quotaCheckpoint(ctx, quotaRead); err != nil {
			return err
		}
		if _, err := approvals.ExpireDue(ctx, tx, now, 200); err != nil {
			return err
		}
		var err error
		list, next, err = approvals.List(ctx, tx, filter, now)
		return err
	})
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return nil, err
		}
		if errors.Is(err, approvals.ErrInvalid) {
			return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil, nil))
		}
		return nil, f.apprFailed(ctx, "store", err)
	}
	return connect.NewResponse(&hubv1.ListApprovalsResponse{Approvals: list, NextPageToken: next}), nil
}

// GetApproval reads one approval. A past-due PENDING approval comes back
// EXPIRED: the read expires it in the same job, audited.
func (f *Fleet) GetApproval(ctx context.Context, req *connect.Request[hubv1.GetApprovalRequest],
) (*connect.Response[hubv1.GetApprovalResponse], error) {
	if _, err := authorizeApprovals(ctx); err != nil {
		return nil, f.apprRefused(ctx, err)
	}
	now := f.hubNow()
	var got *hubv1.Approval
	err := f.store.Write(ctx, func(tx *sql.Tx) error {
		// Zero time asks Get to check the id without lazy expiry. An
		// exhausted quota must not write an expiration audit row.
		if _, err := approvals.Get(ctx, tx, req.Msg.GetId().GetValue(), time.Time{}); err != nil {
			return err
		}
		if err := quotaCheckpoint(ctx, quotaRead); err != nil {
			return err
		}
		var err error
		got, err = approvals.Get(ctx, tx, req.Msg.GetId().GetValue(), now)
		return err
	})
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return nil, err
		}
		if errors.Is(err, approvals.ErrUnknown) {
			return nil, f.apprRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown approval", nil, nil))
		}
		if errors.Is(err, approvals.ErrInvalid) {
			return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "approval id is required", nil, nil))
		}
		return nil, f.apprFailed(ctx, "store", err)
	}
	return connect.NewResponse(&hubv1.GetApprovalResponse{Approval: got}), nil
}

// DecideApproval records one person's decision (02m, 03d W7). Order of
// checks: the caller (scope, then the local approval secret in
// local_trusted mode), the fields, the request_id replay, then the
// approval's own checks. An approved prompt answer or confirmed command is
// enqueued in the decision's transaction; a deny sends nothing and returns
// no operation. A decision on a withdrawn prompt fails PROMPT_GONE.
func (f *Fleet) DecideApproval(ctx context.Context, req *connect.Request[hubv1.DecideApprovalRequest],
) (*connect.Response[hubv1.DecideApprovalResponse], error) {
	who, err := authorizeApprovals(ctx)
	if err != nil {
		return nil, f.apprRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("DecideApproval"), req.Header().Get(SecretHeader)); err != nil {
		// Counted once as secret_refused (not refused): the secret check
		// runs before any approval is touched.
		f.tel.Logger.InfoContext(ctx, "approval.secret_refused", "actor", actor)
		f.approvals.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", "unknown"), attribute.String("outcome", "secret_refused")))
		return nil, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil)
	}
	m := req.Msg
	now := f.hubNow()
	if m.GetRequestId() == "" {
		return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if m.GetExpectedVersion() == 0 {
		return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH, "expected_version names the approval state seen", nil, nil))
	}
	var outcome approvals.Outcome
	var sent *hubv1.Operation
	var sentKind string
	err = f.store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		outcome, err = approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: m.GetId().GetValue(), ExpectedVersion: m.GetExpectedVersion(), Decision: m.GetDecision(),
			Decider: who, Actor: actorOf(who), RequestID: m.GetRequestId(), Now: now,
			BeforeDecision: func(a *hubv1.Approval, promptID string) error {
				cmd, err := commandForDecision(ctx, tx, a, m.GetDecision(), promptID)
				if err != nil || cmd == nil {
					return err
				}
				cmd.RequestedBy = who
				cmd.IssuedTime = timestamppb.New(now)
				return mapOutboxError(outbox.CheckEnqueue(ctx, tx, outbox.In{
					MachineID: a.GetMachine().GetValue(), Command: cmd, RequestID: m.GetRequestId(), Now: now,
				}))
			},
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return err
		}
		if outcome.Replay {
			// A retry returns the stored response, operation included
			// (02b idempotency): nothing is decided or sent twice.
			if outcome.Sends && outcome.OperationID != "" {
				sent, err = loadOperation(ctx, tx, outcome.OperationID)
				if err != nil {
					return err
				}
			}
			return nil
		}
		if !outcome.Sends {
			return nil
		}
		sent, sentKind, err = f.sendDecided(ctx, tx, who, outcome, m.GetRequestId(), now)
		if err != nil {
			return err
		}
		// Reload: the attach stamped the sent operation onto the row.
		outcome.Approval, err = approvals.Get(ctx, tx, outcome.Approval.GetId().GetValue(), now)
		return err
	})
	if err != nil {
		return nil, f.decideStoreError(ctx, err)
	}
	kind := approvalKindAttr(outcome.Approval.GetKind())
	outcomeName := "denied"
	if outcome.Sends {
		outcomeName = "approved"
	}
	f.approvals.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", kind), attribute.String("outcome", outcomeName)))
	if sent != nil && !outcome.Replay {
		f.countSession(ctx, sentKind, "sent")
	}
	f.tel.Logger.InfoContext(ctx, "approval.decided", "kind", kind,
		"approval", outcome.Approval.GetId().GetValue(),
		"sends", outcome.Sends, "replay", outcome.Replay, "actor", actor)
	f.publishApprovalWatch(outcome.HubSeq, outcome.Approval, hubv1.ChangeType_CHANGE_TYPE_UPDATED, outcome.Replay)
	return connect.NewResponse(&hubv1.DecideApprovalResponse{
		Approval: outcome.Approval, Operation: sent,
	}), nil
}

// decideStoreError renders a Decide failure: the replay conflict, the
// closed approval (a withdrawn prompt is PROMPT_GONE, anything else
// APPROVAL_CLOSED), then the field checks. The closed state comes from the
// decision's own transaction (approvals.ClosedError), never a second read.
func (f *Fleet) decideStoreError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, approvals.ErrRequestConflict):
		return f.apprRefused(ctx, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different decision", nil, nil))
	case errors.Is(err, approvals.ErrClosed):
		var closed *approvals.ClosedError
		if errors.As(err, &closed) && closed.State == hubv1.ApprovalState_APPROVAL_STATE_WITHDRAWN {
			return f.apprRefused(ctx, apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_PROMPT_GONE,
				"the prompt left the screen; nothing was typed", nil, nil))
		}
		return f.apprRefused(ctx, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_APPROVAL_CLOSED,
			"the approval is decided or expired", nil, nil))
	case errors.Is(err, approvals.ErrVersion):
		return f.apprRefused(ctx, apiError(connect.CodeAborted,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH, "the approval changed; read it again", nil, nil))
	case errors.Is(err, approvals.ErrRole):
		return f.apprRefused(ctx, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
			"the decider's role does not cover this approval", nil, nil))
	case errors.Is(err, approvals.ErrDecision):
		return f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the decision is not one of the allowed choices", nil, nil))
	case errors.Is(err, approvals.ErrInflight):
		return f.apprRefused(ctx, apiError(connect.CodeAborted,
			typev1.ReasonCode_REASON_CODE_APPROVAL_CLOSED,
			"another answer for this prompt is already on its way; wait for its result", nil, nil))
	case errors.Is(err, approvals.ErrUnknown):
		return f.apprRefused(ctx, apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown approval", nil, nil))
	case errors.Is(err, approvals.ErrInvalid):
		return f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the decision is invalid", nil, nil))
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.apprRefused(ctx, err)
	}
	return f.apprFailed(ctx, "store", err)
}

// approvalKindAttr is the bounded kind label for approval metrics.
func approvalKindAttr(k hubv1.ApprovalKind) string {
	switch k {
	case hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER:
		return "prompt_answer"
	case hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM:
		return "command_confirm"
	case hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM:
		return "dispatch_confirm"
	default:
		return "unknown"
	}
}

// sendDecided enqueues what an approved decision releases, in the Decide
// transaction: the typed answer for a prompt (bound to its prompt_id), the
// stored command for a confirm. Dispatch confirms create nothing here;
// row 22's StartGoal reads the decided approval and starts the goal.
func (f *Fleet) sendDecided(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	outcome approvals.Outcome, requestID string, now time.Time,
) (*hubv1.Operation, string, error) {
	a := outcome.Approval
	cmd, err := commandForDecision(ctx, tx, a, a.GetDecision(), outcome.PromptID)
	if err != nil || cmd == nil {
		return nil, "", err
	}
	return f.sendApprovedTx(ctx, tx, who, a, cmd, requestID, now)
}

func commandForDecision(ctx context.Context, tx *sql.Tx, a *hubv1.Approval,
	decision, promptID string,
) (*agentv1.Command, error) {
	switch a.GetKind() {
	case hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER:
		return &agentv1.Command{
			Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{
				Pane: a.GetPane(),
				Keys: []*typev1.KeyInput{
					{Input: &typev1.KeyInput_Literal{Literal: decision}},
					{Input: &typev1.KeyInput_Key{Key: typev1.NamedKey_NAMED_KEY_ENTER}},
				},
				AnsweringPrompt: &typev1.PromptId{Value: promptID},
				Purpose:         typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_PROMPT_ANSWER,
			}},
		}, nil
	case hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM:
		return approvals.StoredCommand(ctx, tx, a.GetId().GetValue())
	default:
		return nil, nil
	}
}

// gateCommand holds a destructive call for an approval instead of sending
// it (02m "Actions that need an approval"): the caller returns the approval
// and no operation, and DecideApproval sends the stored command once
// approved. A repeated request_id returns the stored approval.
func (f *Fleet) gateCommand(ctx context.Context, who *typev1.Principal, req proto.Message,
	requestID, sessionID string, pane *typev1.PaneKey, preview string, cmd *agentv1.Command,
) (*hubv1.Approval, error) {
	actor := who.GetId().GetValue()
	now := f.hubNow()
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, f.sessFailed(ctx, actor, "encode", err)
	}
	sum := sha256.Sum256(body)
	var approval *hubv1.Approval
	var fresh *hubv1.Approval
	var freshSeq int64
	err = f.store.Write(ctx, func(tx *sql.Tx) error {
		machine, err := resolveSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if id, replay, err := checkGateReplay(ctx, tx, actor, requestID, sum[:]); err != nil || replay {
			if err != nil {
				return err
			}
			// The first read checks the stored id without expiring it.
			if _, err := approvals.Get(ctx, tx, id, time.Time{}); err != nil {
				return err
			}
			if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
				return err
			}
			approval, err = approvals.Get(ctx, tx, id, now)
			return err
		}
		created, seq, err := approvals.CreateApproval(ctx, tx, approvals.Create{
			Kind:      hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM,
			Severity:  hubv1.Severity_SEVERITY_WARNING,
			Preview:   truncatePreview(preview),
			MachineID: machine, SessionID: sessionID, Pane: pane,
			Allowed:       []approvals.Choice{{Key: "approve"}, {Key: "deny"}},
			DefaultChoice: "deny",
			Fallback:      hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
			ExpireTime:    now.Add(approvalTimeout),
			RequiredRole:  typev1.Role_ROLE_OWNER,
			Command:       cmd,
			Now:           now,
			Checkpoint:    func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return err
		}
		if err := logGate(ctx, tx, actor, requestID, created.GetId().GetValue(), sum[:], now); err != nil {
			return err
		}
		approval, fresh = created, created
		freshSeq = seq
		return nil
	})
	if err != nil {
		return nil, f.storeError(ctx, actor, err)
	}
	if fresh != nil {
		f.approvals.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", "command_confirm"), attribute.String("outcome", "created")))
		f.tel.Logger.InfoContext(ctx, "approval.created", "kind", "command_confirm",
			"approval", fresh.GetId().GetValue(), "actor", actor)
		f.publishApprovalWatch(freshSeq, fresh, hubv1.ChangeType_CHANGE_TYPE_CREATED, false)
	}
	return approval, nil
}

// checkGateReplay returns the stored approval when (principal, request_id)
// repeats the gating call.
func checkGateReplay(ctx context.Context, tx *sql.Tx, principal, requestID string, hash []byte) (string, bool, error) {
	var approvalID string
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT approval_id, request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).Scan(&approvalID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("replay check: %w", err)
	}
	if string(stored) != string(hash) {
		return "", false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	return approvalID, true, nil
}

// logGate records the (principal, request_id) → approval mapping.
func logGate(ctx context.Context, tx *sql.Tx, principal, requestID, approvalID string, hash []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO request_log(principal_id, request_id, command_id, approval_id, request_hash, create_time)
		VALUES (?, ?, '', ?, ?, ?)`, principal, requestID, approvalID, hash, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("log gate: %w", err)
	}
	return nil
}

// truncatePreview caps an approval preview at the 2 KiB the contract
// holds, on a rune boundary so multibyte text is never split.
func truncatePreview(s string) string {
	if len(s) <= approvals.MaxPreview {
		return s
	}
	cut := s[:approvals.MaxPreview]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// apprRefused counts and logs an approval caller error and returns it.
func (f *Fleet) apprRefused(ctx context.Context, err error) error {
	f.approvals.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", "unknown"), attribute.String("outcome", "refused")))
	f.tel.Logger.InfoContext(ctx, "approval.issue_refused", "reason", reasonOf(err))
	return err
}

// apprFailed counts and logs the hub's own approval failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) apprFailed(ctx context.Context, step string, cause error) error {
	f.approvals.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", "unknown"), attribute.String("outcome", "failed")))
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "approval.issue_failed", "step", step, "err_kind", kind)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not decide the approval", nil, nil)
}

// hubProcedure is the Connect procedure name of a FleetService method.
func hubProcedure(method string) string {
	return "/fleet.hub.v1.FleetService/" + method
}
