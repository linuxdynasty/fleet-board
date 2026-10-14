package control

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// capabilityF1 is the wire name of the [CAP F1] capability the handler
// advertises in Hello: the capability id lowercased, since the connection
// loop drops names outside [a-z0-9._-]. A Command whose requires names it
// at this version or below is supported once this handler is registered.
var capabilityF1 = &typev1.Capability{Name: "f1", Version: 1}

// sessionHandler is the exec.Handler for EnsureSession. The wiring row
// registers one per server the agent serves.
type sessionHandler struct {
	e *Ensurer
}

// Handler returns the Ensurer as the executor's Handler for EnsureSession.
func (e *Ensurer) Handler() exec.Handler { return sessionHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h sessionHandler) Kind() string { return "EnsureSession" }

// Capabilities are advertised in Hello while the handler is registered.
func (h sessionHandler) Capabilities() []*typev1.Capability {
	return []*typev1.Capability{capabilityF1}
}

// Class re-runs the command from the start: EnsureSession is declarative,
// and a replayed command_id that found its session returns it with
// created=false.
func (h sessionHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h sessionHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureSession)
	if !ok || cmd.EnsureSession == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: EnsureSession handler got another kind",
		}
	}
	return h.e.EnsureSession(ctx, cmd.EnsureSession)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h sessionHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
