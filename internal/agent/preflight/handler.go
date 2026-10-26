package preflight

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// Handler is the exec.Handler for Preflight (02e "Command kinds and
// capabilities"). The run wiring registers it with the production
// Reader (cmd/fleet-agent/startgoalwire.go).
type Handler struct{ Checker *Checker }

// Kind is the Command.kind arm's message name.
func (h Handler) Kind() string { return "Preflight" }

// Capabilities are advertised in Hello while the handler is registered.
func (h Handler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{{Name: "preflight", Version: 1}}
}

// Class re-runs the command from the start: preflight only reads, so a
// replayed command_id is safe.
func (h Handler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run checks without acting. A refusal is still status OK with ok=false
// and the named reasons (03c): only an unreadable fact or a malformed
// command fails the result, and nothing was typed either way.
func (h Handler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_Preflight)
	if !ok || cmd.Preflight == nil || h.Checker == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "preflight: Preflight handler got another kind",
		}
	}
	res, err := h.Checker.Check(ctx, cmd.Preflight.GetSpec())
	if err != nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "preflight: facts unreadable; nothing typed",
		}
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK,
		Reason: typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Detail: &agentv1.CommandResult_Preflight{Preflight: res},
	}
}

// Resume re-runs from the start: there are no steps to continue from.
func (h Handler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
