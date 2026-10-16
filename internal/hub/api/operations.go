package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// GetOperation reads one operation by its command id (02k: exactly what is
// served). The read needs the same local caller as the machine reads: the
// admin socket serves local_trusted callers, and scopes arrive with the
// rest of row 12.
//
// Unserved so far: output_text and output_truncated (the per-kind result
// decode with the LOGS-scope redaction), failed_checks, and trace_id
// (nothing stores them yet); they stay empty until their slice.
func (f *Fleet) GetOperation(ctx context.Context, req *connect.Request[hubv1.GetOperationRequest],
) (*connect.Response[hubv1.GetOperationResponse], error) {
	who, err := authorizeMachines(ctx)
	if err != nil {
		return nil, f.opRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	id := req.Msg.GetId().GetValue()
	if id == "" {
		return nil, f.opRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "operation id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "operation id is required"}}))
	}
	d, err := outbox.Lookup(ctx, f.store, id)
	if err != nil {
		if errors.Is(err, outbox.ErrCommandUnknown) {
			return nil, f.opRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown operation", nil, nil))
		}
		return nil, f.opFailed(ctx, actor, "store", err)
	}
	op, err := operationFromDetail(d)
	if err != nil {
		return nil, f.opFailed(ctx, actor, "store", err)
	}
	return connect.NewResponse(&hubv1.GetOperationResponse{Operation: op}), nil
}

// operationFromDetail builds the served Operation of one stored command.
// A stored status or reason name the contract never defined is the hub's
// failure, INTERNAL: the row came from the hub's own closed set.
func operationFromDetail(d outbox.Detail) (*hubv1.Operation, error) {
	status, err := operationStatus(d.Status)
	if err != nil {
		return nil, err
	}
	reason, err := operationReason(d.Reason)
	if err != nil {
		return nil, err
	}
	op := &hubv1.Operation{
		Id:           &typev1.CommandId{Value: d.CommandID},
		Version:      uint64(d.Version), //nolint:gosec // G115: commands.version starts at 1
		CreateTime:   timestamppb.New(d.Created),
		UpdateTime:   timestamppb.New(d.Updated),
		Machine:      &typev1.MachineId{Value: d.MachineID},
		Kind:         operationKind(d.Kind),
		State:        operationState(d.OpState),
		Status:       status,
		Step:         d.Step,
		StepIndex:    uint32(d.StepIndex), //nolint:gosec // G115: step_index has CHECK >= 0
		StepCount:    uint32(d.StepCount), //nolint:gosec // G115: step_count has CHECK >= 0
		Message:      d.Message,
		RequestId:    d.RequestID,
		NotAfterTime: timestamppb.New(d.NotAfter),
	}
	by, err := operationRequester(d)
	if err != nil {
		return nil, err
	}
	op.RequestedBy = by
	if d.GoalID != "" {
		op.Goal = &typev1.GoalId{Value: d.GoalID}
	}
	if d.HasAck {
		op.DeliveredTime = timestamppb.New(d.AckTime)
	}
	if status != typev1.Status_STATUS_OK && status != typev1.Status_STATUS_UNSPECIFIED {
		failure, err := operationFailure(d, reason)
		if err != nil {
			return nil, err
		}
		op.Error = failure
	}
	return op, nil
}

// operationRequester is the full principal that caused the command, from
// the stored command: the same identity the enqueue response carried, so
// a refresh never narrows it to an id.
func operationRequester(d outbox.Detail) (*typev1.Principal, error) {
	var cmd agentv1.Command
	if err := proto.Unmarshal(d.Command, &cmd); err != nil {
		return nil, fmt.Errorf("api: stored command: %w", err)
	}
	if cmd.GetRequestedBy().GetId().GetValue() == "" {
		return nil, fmt.Errorf("api: stored command has no requester")
	}
	return cmd.GetRequestedBy(), nil
}

// operationFailure is the ErrorDetail of a command that did not end OK:
// the stored reason plus, when the agent's first result arrived, its
// retryable, retry_after and metadata. A hub-closed row (expiry, revoke)
// has no result and carries the reason alone.
func operationFailure(d outbox.Detail, reason typev1.ReasonCode) (*typev1.ErrorDetail, error) {
	out := &typev1.ErrorDetail{Reason: reason}
	if !d.HasResult {
		return out, nil
	}
	var res agentv1.CommandResult
	if err := proto.Unmarshal(d.Result, &res); err != nil {
		return nil, fmt.Errorf("api: stored result: %w", err)
	}
	out.Retryable = res.GetRetryable()
	out.RetryAfter = res.GetRetryAfter()
	out.Metadata = res.GetMetadata()
	return out, nil
}

// operationStatus maps a stored status name back to the contract. Empty is
// an open command, UNSPECIFIED, and the hub's own UNKNOWN sentinel (an
// agent enum this hub has no name for) reads UNSPECIFIED too, so an
// accepted result stays readable. Any other unknown name is the hub's
// failure, INTERNAL.
func operationStatus(name string) (typev1.Status, error) {
	if name == "" || name == "UNKNOWN" {
		return typev1.Status_STATUS_UNSPECIFIED, nil
	}
	if v, ok := typev1.Status_value["STATUS_"+name]; ok {
		return typev1.Status(v), nil
	}
	return typev1.Status_STATUS_UNSPECIFIED, fmt.Errorf("api: unknown operation status %q", name)
}

// operationReason maps a stored reason name back to the contract, with
// the same empty and UNKNOWN rules as the status.
func operationReason(name string) (typev1.ReasonCode, error) {
	if name == "" || name == "UNKNOWN" {
		return typev1.ReasonCode_REASON_CODE_UNSPECIFIED, nil
	}
	if v, ok := typev1.ReasonCode_value["REASON_CODE_"+name]; ok {
		return typev1.ReasonCode(v), nil
	}
	return typev1.ReasonCode_REASON_CODE_UNSPECIFIED, fmt.Errorf("api: unknown operation reason %q", name)
}

// opRefused logs an operation-read caller error and returns it.
func (f *Fleet) opRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "operations.issue_refused", "reason", reasonOf(err))
	return err
}

// opFailed logs the hub's own operation-read failure and returns INTERNAL
// without the cause's text.
func (f *Fleet) opFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "operations.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its operations", nil, nil)
}
