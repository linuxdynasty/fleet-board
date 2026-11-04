package attach

import (
	"context"
	"errors"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// attachPTY is the capability an OpenAttach command requires (02e): the
// agent opens Terminal streams through a PTY-backed tmux attach.
var attachPTY = []*typev1.Capability{{Name: "attach.pty", Version: 1}}

// OpenHandler runs OpenAttach commands: open (or re-open) the attach and
// answer its grouped view session name. The command is declarative, so it
// is RepeatSafe: a replayed command_id returns the live view, and a
// re-open after a teardown opens cleanly.
func (m *Manager) OpenHandler() exec.Handler { return openHandler{m: m} }

// CloseHandler runs CloseAttach commands: end the attach, idempotent, so
// it is RepeatSafe too.
func (m *Manager) CloseHandler() exec.Handler { return closeHandler{m: m} }

type openHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h openHandler) Kind() string { return "OpenAttach" }

// Capabilities are advertised in Hello while the handler is registered.
func (h openHandler) Capabilities() []*typev1.Capability { return attachPTY }

// Class re-runs the command from the start: opening is declarative.
func (h openHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h openHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_OpenAttach)
	if !ok || cmd.OpenAttach == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "attach: OpenAttach handler got another kind")
	}
	view, err := h.m.Open(ctx, cmd.OpenAttach)
	if err != nil {
		return mapOpenError(err)
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Detail: &agentv1.CommandResult_OpenAttach{OpenAttach: &agentv1.OpenAttachResult{ViewSessionName: view}},
	}
}

// Resume re-runs from the start: there are no steps to continue from.
func (h openHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

type closeHandler struct{ m *Manager }

// Kind is the Command.kind arm's message name.
func (h closeHandler) Kind() string { return "CloseAttach" }

// Capabilities are advertised in Hello while the handler is registered.
func (h closeHandler) Capabilities() []*typev1.Capability { return attachPTY }

// Class re-runs the command from the start: closing is idempotent.
func (h closeHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. Unknown ids succeed:
// closes are idempotent, and the hub sends CloseAttach as a fallback for
// attaches the agent already tore down.
func (h closeHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_CloseAttach)
	if !ok || cmd.CloseAttach == nil {
		return failed(typev1.ReasonCode_REASON_CODE_INTERNAL, "attach: CloseAttach handler got another kind")
	}
	if id := cmd.CloseAttach.GetAttachId().GetValue(); id == "" {
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "attach: close names no attach")
	} else {
		h.m.Close(id)
	}
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_OK}
}

// Resume re-runs from the start: closing is idempotent.
func (h closeHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// failed is a FAILED result without retry: the request itself is bad.
func failed(why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED, Reason: why, Message: msg}
}

// mapOpenError renders an Open failure: malformed opens and missing
// sessions fail closed, while a down tmux server is retryable (the hub
// sends the command again once the server is back).
func mapOpenError(err error) *agentv1.CommandResult {
	switch {
	case errors.Is(err, ErrInvalid):
		return failed(typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, trimErr(err))
	case errors.Is(err, ErrNoSession):
		return failed(typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND, trimErr(err))
	default:
		out := failed(typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN, trimErr(err))
		out.Retryable = true
		return out
	}
}

// trimErr caps a result message: only closed values (ids, names, reasons)
// ever reach it, but tmux one-liners ride along and results cap at 1 KiB.
func trimErr(err error) string {
	const max = 512
	if msg := err.Error(); len(msg) > max {
		return msg[:max]
	} else {
		return msg
	}
}
