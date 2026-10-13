package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
)

// authorizeApprovals admits the caller for ListApprovals, GetApproval and
// DecideApproval (02d: READ|APPROVALS for the reads, APPROVALS to decide).
// Until plan row 12 brings caller scopes, that is only the local user, who
// maps to OWNER and covers every required_role below.
func authorizeApprovals(ctx context.Context) (*typev1.Principal, error) {
	p := PrincipalFrom(ctx)
	if p.GetId().GetValue() == "" {
		return nil, apiError(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"no caller identity", nil, nil)
	}
	if p.GetKind() != typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER {
		return nil, apiError(connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING,
			"this call needs the APPROVALS scope", map[string]string{"scope": "APPROVALS"}, nil)
	}
	return p, nil
}

// ListApprovals lists approvals newest first. Filter and order_by follow
// 02c once row 12 lands that query language; until then only the empty
// filter reads everything.
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
	var list []*hubv1.Approval
	err := f.store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		list, err = approvals.List(ctx, tx, approvals.Filter{PageSize: int(size), PageToken: m.GetPageToken()})
		return err
	})
	if err != nil {
		if errors.Is(err, approvals.ErrInvalid) {
			return nil, f.apprRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil, nil))
		}
		return nil, f.apprFailed(ctx, "store", err)
	}
	return connect.NewResponse(&hubv1.ListApprovalsResponse{Approvals: list}), nil
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
		var err error
		got, err = approvals.Get(ctx, tx, req.Msg.GetId().GetValue(), now)
		return err
	})
	if err != nil {
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
		f.tel.Logger.InfoContext(ctx, "approval.secret_refused", "actor", actor)
		f.approvals.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", "unknown"), attribute.String("outcome", "secret_refused")))
		return nil, f.apprRefused(ctx, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
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
	err = f.store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		outcome, err = approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: m.GetId().GetValue(), ExpectedVersion: m.GetExpectedVersion(), Decision: m.GetDecision(),
			Decider: who, Actor: actorOf(who), RequestID: m.GetRequestId(), Now: now,
		})
		if err != nil {
			return err
		}
		if outcome.Replay || !outcome.Sends {
			return nil
		}
		sent, err = f.sendDecided(ctx, tx, who, outcome, m.GetRequestId(), now)
		if err != nil {
			return err
		}
		// Reload: the attach stamped the sent operation onto the row.
		outcome.Approval, err = approvals.Get(ctx, tx, outcome.Approval.GetId().GetValue(), now)
		return err
	})
	if err != nil {
		return nil, f.decideStoreError(ctx, actor, m.GetId().GetValue(), err)
	}
	kind := approvalKindAttr(outcome.Approval.GetKind())
	outcomeName := "denied"
	if outcome.Sends {
		outcomeName = "approved"
	}
	f.approvals.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", kind), attribute.String("outcome", outcomeName)))
	f.tel.Logger.InfoContext(ctx, "approval.decided", "kind", kind,
		"approval", outcome.Approval.GetId().GetValue(),
		"sends", outcome.Sends, "replay", outcome.Replay, "actor", actor)
	return connect.NewResponse(&hubv1.DecideApprovalResponse{
		Approval: outcome.Approval, Operation: sent,
	}), nil
}

// decideStoreError renders a Decide failure: the replay conflict, the
// closed approval (a withdrawn prompt is PROMPT_GONE, anything else
// APPROVAL_CLOSED), then the field checks.
func (f *Fleet) decideStoreError(ctx context.Context, actor, id string, err error) error {
	switch {
	case errors.Is(err, approvals.ErrRequestConflict):
		return f.apprRefused(ctx, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different decision", nil, nil))
	case errors.Is(err, approvals.ErrClosed):
		if f.approvalWithdrawn(ctx, id) {
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
			typev1.ReasonCode_REASON_CODE_PROMPT_GONE,
			"another answer for this prompt is already on its way", nil, nil))
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

// approvalWithdrawn reports whether id names a withdrawn prompt approval.
func (f *Fleet) approvalWithdrawn(ctx context.Context, id string) bool {
	var state string
	_ = f.store.Read(ctx, func(tx *sql.Tx) error {
		_ = tx.QueryRowContext(ctx, `SELECT state FROM approvals WHERE id = ?`, id).Scan(&state)
		return nil
	})
	return state == "WITHDRAWN"
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
) (*hubv1.Operation, error) {
	a := outcome.Approval
	switch a.GetKind() {
	case hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER:
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{
				Pane: a.GetPane(),
				Keys: []*typev1.KeyInput{
					{Input: &typev1.KeyInput_Literal{Literal: a.GetDecision()}},
					{Input: &typev1.KeyInput_Key{Key: typev1.NamedKey_NAMED_KEY_ENTER}},
				},
				AnsweringPrompt: &typev1.PromptId{Value: outcome.PromptID},
				Purpose:         typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_PROMPT_ANSWER,
			}},
		}
		return f.sendApprovedTx(ctx, tx, who, a, cmd, requestID, now)
	case hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM:
		cmd, err := approvals.StoredCommand(ctx, tx, a.GetId().GetValue())
		if err != nil {
			return nil, err
		}
		return f.sendApprovedTx(ctx, tx, who, a, cmd, requestID, now)
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
	err = f.store.Write(ctx, func(tx *sql.Tx) error {
		machine, err := resolveSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if id, replay, err := checkGateReplay(ctx, tx, actor, requestID, sum[:]); err != nil || replay {
			if err != nil {
				return err
			}
			approval, err = approvals.Get(ctx, tx, id, now)
			return err
		}
		created, err := approvals.CreateApproval(ctx, tx, approvals.Create{
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
		})
		if err != nil {
			return err
		}
		if err := logGate(ctx, tx, actor, requestID, created.GetId().GetValue(), sum[:], now); err != nil {
			return err
		}
		approval = created
		f.approvals.Add(ctx, 1, metric.WithAttributes(
			attribute.String("kind", "command_confirm"), attribute.String("outcome", "created")))
		f.tel.Logger.InfoContext(ctx, "approval.created", "kind", "command_confirm",
			"approval", created.GetId().GetValue(), "actor", actor)
		return nil
	})
	if err != nil {
		return nil, f.storeError(ctx, actor, err)
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

// truncatePreview caps an approval preview at the 2 KiB the contract holds.
func truncatePreview(s string) string {
	if len(s) > approvals.MaxPreview {
		return s[:approvals.MaxPreview]
	}
	return s
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
