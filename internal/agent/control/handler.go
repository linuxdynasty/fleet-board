package control

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// capabilitiesFor is the [CAP F1] capability set the handler advertises in
// Hello (02e "Command kinds and capabilities"): tmux.control plus the
// server-specific one, so a Command whose requires names either at this
// version or below is supported once this handler is registered. The probe
// server has no specific name in 02e, so it advertises tmux.control alone.
func capabilitiesFor(kind typev1.TmuxServerKind) []*typev1.Capability {
	caps := []*typev1.Capability{{Name: "tmux.control", Version: 1}}
	switch kind {
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT:
		caps = append(caps, &typev1.Capability{Name: "tmux.default_server.control", Version: 1})
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET:
		caps = append(caps, &typev1.Capability{Name: "tmux.fleet_server", Version: 1})
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE,
		typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED:
		// No specific name: control alone. UNSPECIFIED never reaches
		// here; New refuses it.
	}
	return caps
}

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
	return capabilitiesFor(h.e.kind)
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
