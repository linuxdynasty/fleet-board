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
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
)

// countAttachTicket counts a CreateAttachTicket call by its result.
func (f *Fleet) countAttachTicket(ctx context.Context, result string) {
	f.attachTickets.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// countAttach counts an AttachTerminal session by its result; closed
// sessions also name their reason.
func (f *Fleet) countAttach(ctx context.Context, result, reason string) {
	f.attaches.Add(ctx, 1, metric.WithAttributes(
		attribute.String("result", result), attribute.String("reason", reason)))
}

// attachRefused counts and logs a caller error by its reason and returns it.
func (f *Fleet) attachRefused(ctx context.Context, actor string, err error) error {
	f.countAttachTicket(ctx, "refused")
	f.tel.Logger.InfoContext(ctx, "attach.issue_refused", "reason", reasonOf(err), "actor", actor)
	return err
}

// attachStoreError renders a ticket writer-job failure: replays keep
// their caller-error shape (counted delivered or conflict), other caller
// errors count refused, and the hub's own become INTERNAL.
func (f *Fleet) attachStoreError(ctx context.Context, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		result := "refused"
		switch reasonOf(err) {
		case typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED.String():
			result = "delivered"
		case typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT.String():
			result = "conflict"
		}
		f.countAttachTicket(ctx, result)
		f.tel.Logger.InfoContext(ctx, "attach.ticket_refused", "reason", reasonOf(err), "actor", actor)
		return err
	}
	return f.attachFailed(ctx, actor, "store", err)
}

// attachFailed counts and logs the hub's own failure and returns INTERNAL.
func (f *Fleet) attachFailed(ctx context.Context, actor, step string, cause error) error {
	f.countAttachTicket(ctx, "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "attach.issue_failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not create the ticket", nil, nil)
}

// CreateAttachTicket mints a single-use WebSocket ticket (02f): 256-bit
// random, valid 60 s, bound to the caller, the session, the read-only
// flag and the request's Origin, and shown once. A replay of the same
// request_id never re-shows it: the same body is SECRET_ALREADY_DELIVERED
// and another body REQUEST_ID_CONFLICT, both ALREADY_EXISTS. A caller
// without SESSIONS_WRITE is clamped to read-only. Order of checks, as
// 02b ranks them: the caller, the fields, the request_id.
func (f *Fleet) CreateAttachTicket(ctx context.Context, req *connect.Request[hubv1.CreateAttachTicketRequest],
) (*connect.Response[hubv1.CreateAttachTicketResponse], error) {
	who, err := authorizeScope(ctx, "READ")
	if err != nil {
		return nil, f.attachRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetRequestId() == "" {
		return nil, f.attachRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	sessionID := m.GetSession().GetValue()
	if sessionID == "" {
		return nil, f.attachRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "session is required", nil,
			[]*typev1.FieldViolation{{Field: "session", Description: "session is required"}}))
	}
	readOnly := m.GetReadOnly()
	if !readOnly {
		if _, werr := authorizeScope(ctx, "SESSIONS_WRITE"); werr != nil {
			readOnly = true
		}
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, f.attachFailed(ctx, actor, "encode", err)
	}
	sum := sha256.Sum256(body)
	origin := req.Header().Get("Origin")
	now := f.hubNow()
	var secret string
	var expires time.Time
	doneCh, submitErr := f.store.SubmitHook(ctx, func(tx *sql.Tx) error {
		if _, err := resolveSession(ctx, tx, sessionID); err != nil {
			return err
		}
		var stored []byte
		err := tx.QueryRowContext(ctx, `SELECT request_hash FROM request_log
			WHERE principal_id = ? AND request_id = ?`, actor, m.GetRequestId()).Scan(&stored)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("attach: replay check: %w", err)
		case string(stored) == string(sum[:]):
			return apiError(connect.CodeAlreadyExists,
				typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED,
				"this request_id already delivered its ticket, which is never shown again; make a new request", nil, nil)
		default:
			return apiError(connect.CodeAlreadyExists,
				typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
				"this request_id was used for a different request", nil, nil)
		}
		if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
			return err
		}
		var merr error
		secret, expires, merr = f.attach.MintTicket(attach.MintIn{
			PrincipalID: actor, SessionID: sessionID, RequestID: m.GetRequestId(),
			ReadOnly: readOnly, Origin: origin, Body: body,
		})
		if merr != nil {
			return fmt.Errorf("attach: mint ticket: %w", merr)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_log(principal_id, request_id, command_id, request_hash, create_time)
			VALUES (?, ?, ?, ?, ?)`, actor, m.GetRequestId(), "", sum[:], now.UnixMilli()); err != nil {
			return fmt.Errorf("attach: log request: %w", err)
		}
		return nil
		// The hook runs on the writer in commit order; tickets publish
		// nothing (the response carries the ticket), so it is empty.
	}, func() {})
	if submitErr != nil {
		return nil, f.attachStoreError(ctx, actor, submitErr)
	}
	// A submitted job can commit after the RPC context is cancelled.
	// Always await the writer's definitive outcome. A rollback after the
	// mint un-mints the ticket, so a retry starts clean.
	if commitErr := <-doneCh; commitErr != nil {
		if secret != "" {
			f.attach.RevokeTicket(secret, actor, m.GetRequestId())
		}
		return nil, f.attachStoreError(ctx, actor, commitErr)
	}
	f.countAttachTicket(ctx, "issued")
	f.tel.Logger.InfoContext(ctx, "attach.ticket_issued", "session", sessionID,
		"read_only", readOnly, "actor", actor)
	return connect.NewResponse(&hubv1.CreateAttachTicketResponse{
		Ticket: secret, ExpireTime: timestamppb.New(expires), ReadOnly: readOnly,
	}), nil
}
