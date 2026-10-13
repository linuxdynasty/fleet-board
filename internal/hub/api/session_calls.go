package api

import (
	"context"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// SendKeys sends SendKeys (F3) with purpose OPERATOR_TEXT: free text an
// operator types as a hint. Not for prompt answers: those go through
// DecideApproval, so every answer is bound to its prompt_id and audited
// once.
func (f *Fleet) SendKeys(ctx context.Context, req *connect.Request[hubv1.SendKeysRequest],
) (*connect.Response[hubv1.SendKeysResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	m := req.Msg
	if len(m.GetKeys()) == 0 {
		return nil, f.sessRefused(ctx, who.GetId().GetValue(), apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "keys is required", nil, nil))
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{
			Pane: m.GetPane(), Keys: m.GetKeys(),
			Purpose: typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT,
		}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.keys", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.SendKeysResponse{Operation: op}), nil
}

// InterruptPane sends Interrupt (F4).
func (f *Fleet) InterruptPane(ctx context.Context, req *connect.Request[hubv1.InterruptPaneRequest],
) (*connect.Response[hubv1.InterruptPaneResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	m := req.Msg
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: m.GetPane()}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.interrupt", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.InterruptPaneResponse{Operation: op}), nil
}

// RunHarnessCommand sends RunHarnessCommand (F8). A DESTRUCTIVE-class
// command returns an Approval and no operation: nothing is sent, and no
// Operation exists, until the approval is approved (02m). A DENY-class
// command is refused: deny-class commands are never sent.
func (f *Fleet) RunHarnessCommand(ctx context.Context, req *connect.Request[hubv1.RunHarnessCommandRequest],
) (*connect.Response[hubv1.RunHarnessCommandResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetRequestId() == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if !hasSlashPrefix(m.GetCommand()) {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "command carries the leading slash", nil, nil))
	}
	if m.GetExpectedClass() == typev1.CommandClass_COMMAND_CLASS_UNSPECIFIED {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "expected_class is required", nil, nil))
	}
	if m.GetExpectedClass() == typev1.CommandClass_COMMAND_CLASS_DENY {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_COMMAND_CLASS_DENIED, "deny-class commands are never sent", nil, nil))
	}
	if m.GetExpectedClass() == typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE && m.GetArgs() != "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "a destructive command carries no args", nil, nil))
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{
			Pane: m.GetPane(), Command: m.GetCommand(), Args: m.GetArgs(),
			ExpectedClass: m.GetExpectedClass(), CatalogHash: m.GetCatalogHash(),
		}},
	}
	if m.GetExpectedClass() == typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE {
		approval, err := f.gateCommand(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), m.GetPane(),
			"destructive harness command "+m.GetCommand(), cmd)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&hubv1.RunHarnessCommandResponse{Approval: approval.GetId()}), nil
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.harness_command", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.RunHarnessCommandResponse{Operation: op}), nil
}

// SetPaneAvailability sends SetPaneAvailability (E11) with the owner's
// mark: available panes report MARKED_ON_BOARD, cleared ones
// CLEARED_ON_BOARD.
func (f *Fleet) SetPaneAvailability(ctx context.Context, req *connect.Request[hubv1.SetPaneAvailabilityRequest],
) (*connect.Response[hubv1.SetPaneAvailabilityResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	m := req.Msg
	reason := typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD
	if m.GetAvailableForWork() {
		reason = typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_SetPaneAvailability{SetPaneAvailability: &agentv1.SetPaneAvailability{
			Pane: m.GetPane(), AvailableForWork: m.GetAvailableForWork(), Reason: reason,
			FreshContextOnReuse: m.GetFreshContextOnReuse(),
		}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.availability", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.SetPaneAvailabilityResponse{Operation: op}), nil
}

// RespawnPane sends RespawnPane (F5). A pane that needs the local owner's
// say-so waits for it on the machine (01f L3); the hub sends and returns.
func (f *Fleet) RespawnPane(ctx context.Context, req *connect.Request[hubv1.RespawnPaneRequest],
) (*connect.Response[hubv1.RespawnPaneResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	m := req.Msg
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_RespawnPane{RespawnPane: &agentv1.RespawnPane{
			Pane: m.GetPane(), Resume: m.GetResume(),
		}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.respawn", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.RespawnPaneResponse{Operation: op}), nil
}

// KillWindow sends KillWindow (F6) to the fleet's own windows (sessions it
// started). A developer's window needs an approval first (02m), which needs
// the session mirror to route to it: until row 5 lands that mirror, such a
// call fails SESSION_NOT_FOUND instead of guessing a machine.
func (f *Fleet) KillWindow(ctx context.Context, req *connect.Request[hubv1.KillWindowRequest],
) (*connect.Response[hubv1.KillWindowResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	m := req.Msg
	if m.GetRequestId() == "" {
		return nil, f.sessRefused(ctx, who.GetId().GetValue(), apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if m.GetSession().GetValue() == "" {
		return nil, f.sessRefused(ctx, who.GetId().GetValue(), apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "session is required", nil, nil))
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{Window: m.GetWindow()}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.kill_window", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.KillWindowResponse{Operation: op}), nil
}

func hasSlashPrefix(s string) bool {
	return len(s) > 1 && s[0] == '/'
}
