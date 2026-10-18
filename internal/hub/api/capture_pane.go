package api

import (
	"context"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// maxCaptureBytes caps CapturePaneRequest.max_bytes: the agent clamps to
// 256 KiB (command_tmux.proto) and 02b caps capture output_text there.
const maxCaptureBytes = 256 * 1024

// authorizeCapturePane admits the checked admin user or local TCP owner
// for CapturePane (02d: READ for the visible screen, LOGS for scrollback).
func authorizeCapturePane(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// CapturePane sends CapturePane (E10): the visible screen (last_lines 0)
// or scrollback (last_lines above 0, LOGS). Like the other session calls it
// routes only to sessions the hub started until the tmux mirror lands, and
// audits session.capture with the operation.
func (f *Fleet) CapturePane(ctx context.Context, req *connect.Request[hubv1.CapturePaneRequest],
) (*connect.Response[hubv1.CapturePaneResponse], error) {
	who, err := authorizeCapturePane(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if m.GetPane() == nil || m.GetPane().GetPaneId() == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "pane is required", nil,
			[]*typev1.FieldViolation{{Field: "pane", Description: "pane is required"}}))
	}
	if m.GetMaxBytes() > maxCaptureBytes {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "max_bytes is above 256 KiB", nil,
			[]*typev1.FieldViolation{{Field: "max_bytes", Description: "max_bytes is above 256 KiB"}}))
	}
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_CapturePane{CapturePane: &agentv1.CapturePane{
			Pane: m.GetPane(), LastLines: m.GetLastLines(), KeepAnsi: m.GetKeepAnsi(), MaxBytes: m.GetMaxBytes(),
		}},
	}
	op, err := f.sendOne(ctx, who, m, m.GetRequestId(), m.GetSession().GetValue(), "session.capture", cmd)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.CapturePaneResponse{Operation: op}), nil
}
