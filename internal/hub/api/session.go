package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

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
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// approvalTimeout is HubSettings.approval_timeout (defaults.md): a pending
// approval gets the DENY fallback past this. It is a constant until plan
// row 13a lands the settings store the value lives in.
const approvalTimeout = 30 * time.Minute

// sessionNameRule is the CreateSession name rule (session.proto field 3).
var sessionNameRule = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// authorizeSessionsWrite admits the checked admin user or local TCP owner
// for SESSIONS_WRITE session calls (02d).
func authorizeSessionsWrite(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "SESSIONS_WRITE")
}

// actorOf is the audit actor for a principal.
func actorOf(p *typev1.Principal) statedb.Actor {
	return statedb.Actor{
		ID: p.GetId().GetValue(), Kind: roleKindName(p.GetKind()), Name: p.GetName(),
	}
}

// roleKindName is the audit spelling of a principal kind.
func roleKindName(k typev1.PrincipalKind) string {
	switch k {
	case typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER:
		return "LOCAL_USER"
	case typev1.PrincipalKind_PRINCIPAL_KIND_USER:
		return "USER"
	case typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN:
		return "API_TOKEN"
	case typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE:
		return "DEVICE"
	default:
		return "UNKNOWN"
	}
}

// hubNow is the hub clock at a decision, millisecond precision like the
// store's integer times.
func (f *Fleet) hubNow() time.Time {
	return f.now().UTC().Truncate(time.Millisecond)
}

// sendOne validates a pane-control call, resolves the session's machine,
// replays the request_id, enqueues the agent command and audits the action,
// all in one writer job. The decision and its command commit together or
// not at all (03f); the command is handed to the connection writer after
// commit by the outbox service.
func (f *Fleet) sendOne(ctx context.Context, who *typev1.Principal, req proto.Message,
	requestID, sessionID, auditAction string, cmd *agentv1.Command,
) (*hubv1.Operation, error) {
	actor := who.GetId().GetValue()
	now := f.hubNow()
	if requestID == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if sessionID == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "session is required", nil, nil))
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, f.sessFailed(ctx, actor, "encode", err)
	}
	sum := sha256.Sum256(body)
	var out *hubv1.Operation
	// Telemetry waits for the commit: a job that rolls back must not
	// leave a "sent" count or log behind.
	var emitKind, emitOutcome, emitMachine, emitCommand string
	var queued outbox.Op
	doneCh, submitErr := f.store.SubmitHook(ctx, func(tx *sql.Tx) error {
		machine, err := resolveSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		if op, replay, err := checkRequest(ctx, tx, actor, requestID, sum[:]); err != nil || replay {
			if err != nil {
				return err
			}
			if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
				return err
			}
			out = op
			emitKind, emitOutcome = outbox.KindName(cmd), "replayed"
			return nil
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		op, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machine, Command: cmd, RequestID: requestID, Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		if err := logRequest(ctx, tx, actor, requestID, op.CommandID, sum[:], now); err != nil {
			return err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: auditAction, Target: "sessions/" + sessionID,
			CommandID: op.CommandID, RequestID: requestID, Now: now,
		}); err != nil {
			return err
		}
		out, err = operationFromOp(ctx, tx, op, machine, who, requestID, now)
		if err != nil {
			return err
		}
		queued = op
		emitKind, emitOutcome, emitMachine, emitCommand = op.Kind, "sent", machine, op.CommandID
		return nil
	}, func() {
		// Runs on the writer in commit order (#393).
		// A replayed gating sends nothing new: only a fresh
		// queue publishes its operation.
		if queued.Created {
			f.publishOperationWatch(queued.HubSeq, out)
		}
	})
	if submitErr != nil {
		return nil, f.storeError(ctx, actor, submitErr)
	}
	// A submitted job can commit after the RPC context is cancelled.
	// Always await the writer's definitive outcome before reading its
	// outputs and waking delivery of a new row. The hook already ran:
	// flush publishes before it signals done.
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.storeError(ctx, actor, commitErr)
	}
	if queued.Created && f.onCommandQueued != nil {
		f.onCommandQueued(queued)
	}
	f.countSession(ctx, emitKind, emitOutcome)
	if emitOutcome == "sent" {
		f.tel.Logger.InfoContext(ctx, "session.sent", "kind", emitKind,
			"machine", emitMachine, "command", emitCommand, "actor", actor)
	}
	return out, nil
}

// sendApprovedTx enqueues the command an approval released and attaches it
// to the approval, inside the Decide transaction the caller owns. It
// returns the sent command's kind so the caller counts it after commit.
func (f *Fleet) sendApprovedTx(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	approval *hubv1.Approval, cmd *agentv1.Command, requestID string, now time.Time,
) (*hubv1.Operation, string, error) {
	cmd.RequestedBy = who
	cmd.IssuedTime = timestamppb.New(now)
	op, err := outbox.Enqueue(ctx, tx, outbox.In{
		MachineID: approval.GetMachine().GetValue(), Command: cmd, RequestID: requestID, Now: now,
	})
	if err != nil {
		return nil, "", mapOutboxError(err)
	}
	if err := approvals.AttachOperation(ctx, tx, approval.GetId().GetValue(), op.CommandID, now); err != nil {
		return nil, "", err
	}
	queued, err := operationFromOp(ctx, tx, op, approval.GetMachine().GetValue(), who, requestID, now)
	return queued, op.Kind, err
}

// storeError renders a writer-job failure: caller errors stay as refused,
// the hub's own become INTERNAL.
func (f *Fleet) storeError(ctx context.Context, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.sessRefused(ctx, actor, err)
	}
	return f.sessFailed(ctx, actor, "store", err)
}

// resolveSession maps a hub-assigned session id to its machine. Sessions
// the hub did not start are not routable yet: the tmux mirror (row 5, row
// 7h) maps them once it lands, and until then they fail SESSION_NOT_FOUND.
func resolveSession(ctx context.Context, tx *sql.Tx, sessionID string) (string, error) {
	var machine string
	err := tx.QueryRowContext(ctx, `SELECT machine_id FROM hub_sessions WHERE session_id = ?`, sessionID).Scan(&machine)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"the hub did not start this session and cannot route to it yet", nil, nil)
	case err != nil:
		return "", fmt.Errorf("resolve session: %w", err)
	case machine == "":
		return "", apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"the hub did not start this session and cannot route to it yet", nil, nil)
	}
	return machine, nil
}

// checkRequest replays a (principal, request_id) the hub already sent: the
// stored Operation comes back and nothing is sent twice (02b idempotency).
// The same key with another body is REQUEST_ID_CONFLICT.
func checkRequest(ctx context.Context, tx *sql.Tx, principal, requestID string, hash []byte) (*hubv1.Operation, bool, error) {
	var commandID string
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT command_id, request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, principal, requestID).Scan(&commandID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("replay check: %w", err)
	}
	if string(stored) != string(hash) {
		return nil, false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	op, err := loadOperation(ctx, tx, commandID)
	if err != nil {
		return nil, false, err
	}
	return op, true, nil
}

// logRequest records the (principal, request_id) → command mapping.
func logRequest(ctx context.Context, tx *sql.Tx, principal, requestID, commandID string, hash []byte, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO request_log(principal_id, request_id, command_id, request_hash, create_time)
		VALUES (?, ?, ?, ?, ?)`, principal, requestID, commandID, hash, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("log request: %w", err)
	}
	return nil
}

// loadOperation rebuilds the stored Operation of a replayed command.
func loadOperation(ctx context.Context, tx *sql.Tx, commandID string) (*hubv1.Operation, error) {
	var machine, kind, opState, requestID, byID string
	var version, createMillis, notAfterMillis int64
	err := tx.QueryRowContext(ctx, `SELECT machine_id, kind, version, op_state, create_time, not_after_time,
		request_id, principal_id FROM commands WHERE command_id = ?`, commandID).
		Scan(&machine, &kind, &version, &opState, &createMillis, &notAfterMillis, &requestID, &byID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("replay: command %s is gone", commandID)
	}
	if err != nil {
		return nil, fmt.Errorf("replay: load command: %w", err)
	}
	return &hubv1.Operation{
		Id:           &typev1.CommandId{Value: commandID},
		Version:      uint64(version), //nolint:gosec // G115: commands.version starts at 1
		CreateTime:   timestamppb.New(millisTime(createMillis)),
		UpdateTime:   timestamppb.New(millisTime(createMillis)),
		Machine:      &typev1.MachineId{Value: machine},
		Kind:         operationKind(kind),
		State:        operationState(opState),
		RequestedBy:  &typev1.Principal{Id: &typev1.PrincipalId{Value: byID}},
		RequestId:    requestID,
		NotAfterTime: timestamppb.New(millisTime(notAfterMillis)),
	}, nil
}

func millisTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// operationKind maps an outbox kind arm name to the Operation kind.
func operationKind(arm string) hubv1.OperationKind {
	switch arm {
	case "ensure_session":
		return hubv1.OperationKind_OPERATION_KIND_ENSURE_SESSION
	case "send_keys":
		return hubv1.OperationKind_OPERATION_KIND_SEND_KEYS
	case "interrupt":
		return hubv1.OperationKind_OPERATION_KIND_INTERRUPT
	case "respawn_pane":
		return hubv1.OperationKind_OPERATION_KIND_RESPAWN_PANE
	case "kill_window":
		return hubv1.OperationKind_OPERATION_KIND_KILL_WINDOW
	case "capture_pane":
		return hubv1.OperationKind_OPERATION_KIND_CAPTURE_PANE
	case "run_harness_command":
		return hubv1.OperationKind_OPERATION_KIND_RUN_HARNESS_COMMAND
	case "set_pane_availability":
		return hubv1.OperationKind_OPERATION_KIND_SET_PANE_AVAILABILITY
	default:
		return hubv1.OperationKind_OPERATION_KIND_UNSPECIFIED
	}
}

// operationState maps an outbox op_state to the Operation state.
func operationState(s string) hubv1.OperationState {
	switch s {
	case "DELIVERED":
		return hubv1.OperationState_OPERATION_STATE_DELIVERED
	case "RUNNING":
		return hubv1.OperationState_OPERATION_STATE_RUNNING
	case "DONE":
		return hubv1.OperationState_OPERATION_STATE_DONE
	default:
		return hubv1.OperationState_OPERATION_STATE_PENDING
	}
}

// operationFromOp builds the response from the queued command's deadline.
// Enqueue fills that deadline on a clone, leaving the caller's command unset.
func operationFromOp(ctx context.Context, tx *sql.Tx, op outbox.Op, machine string,
	by *typev1.Principal, requestID string, now time.Time,
) (*hubv1.Operation, error) {
	stored, err := loadOperation(ctx, tx, op.CommandID)
	if err != nil {
		return nil, err
	}
	return &hubv1.Operation{
		Id:           &typev1.CommandId{Value: op.CommandID},
		Version:      uint64(op.Version), //nolint:gosec // G115: commands.version starts at 1
		CreateTime:   timestamppb.New(now),
		UpdateTime:   timestamppb.New(now),
		Machine:      &typev1.MachineId{Value: machine},
		Kind:         operationKind(op.Kind),
		State:        operationState(op.OpState),
		RequestedBy:  by,
		RequestId:    requestID,
		NotAfterTime: stored.GetNotAfterTime(),
	}, nil
}

// mapOutboxError renders an enqueue refusal as a Connect error.
func mapOutboxError(err error) error {
	switch {
	case errors.Is(err, outbox.ErrMachineUnknown):
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"unknown machine", nil, nil)
	case errors.Is(err, outbox.ErrMachineRevoked):
		return apiError(connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_MACHINE_REVOKED,
			"the machine is revoked", nil, nil)
	case errors.Is(err, outbox.ErrConflict):
		return apiError(connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	case errors.Is(err, outbox.ErrInvalid):
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the command is invalid", nil, nil)
	default:
		return err
	}
}

// countSession counts a session-control send by kind and result.
func (f *Fleet) countSession(ctx context.Context, kind, result string) {
	f.sessCmds.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", kind), attribute.String("result", result)))
}

// sessRefused counts and logs a caller error by its reason and returns it.
func (f *Fleet) sessRefused(ctx context.Context, actor string, err error) error {
	f.countSession(ctx, "unknown", "refused")
	f.tel.Logger.InfoContext(ctx, "session.issue_refused", "reason", reasonOf(err), "actor", actor)
	return err
}

// sessFailed counts and logs the hub's own failure and returns INTERNAL
// without the cause's text.
func (f *Fleet) sessFailed(ctx context.Context, actor, step string, cause error) error {
	f.countSession(ctx, "unknown", "failed")
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "session.issue_failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not send the command", nil, nil)
}

// reasonOf is the ErrorDetail reason name of a Connect error.
func reasonOf(err error) string {
	reason := "UNSPECIFIED"
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if ed, ok := v.(*typev1.ErrorDetail); ok {
					reason = ed.GetReason().String()
				}
			}
		}
	}
	return reason
}
