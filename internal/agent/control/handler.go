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

// windowHandler is the exec.Handler for EnsureWindow. The wiring row
// registers one per server the agent serves, next to the session handler.
type windowHandler struct {
	e *Ensurer
}

// WindowHandler returns the Ensurer as the executor's Handler for
// EnsureWindow.
func (e *Ensurer) WindowHandler() exec.Handler { return windowHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h windowHandler) Kind() string { return "EnsureWindow" }

// Capabilities are advertised in Hello while the handler is registered.
func (h windowHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class re-runs the command from the start: EnsureWindow is declarative,
// and a replayed command_id that found its window returns it with
// created=false.
func (h windowHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h windowHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_EnsureWindow)
	if !ok || cmd.EnsureWindow == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: EnsureWindow handler got another kind",
		}
	}
	return h.e.EnsureWindow(ctx, cmd.EnsureWindow)
}

// Resume re-runs from the start: there are no steps to continue from.
func (h windowHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// sendHandler is the exec.Handler for SendKeys. The wiring row registers
// one per server the agent serves, next to the session and window handlers.
type sendHandler struct {
	e *Ensurer
}

// SendKeysHandler returns the Ensurer as the executor's Handler for
// SendKeys.
func (e *Ensurer) SendKeysHandler() exec.Handler { return sendHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h sendHandler) Kind() string { return "SendKeys" }

// Capabilities are advertised in Hello while the handler is registered.
func (h sendHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class never re-runs: SendKeys is not idempotent, and after an agent
// restart a running SendKeys ends UNKNOWN_OUTCOME (02-contracts).
func (h sendHandler) Class(*agentv1.Command) exec.Class { return exec.NotRepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h sendHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_SendKeys)
	if !ok || cmd.SendKeys == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: SendKeys handler got another kind",
		}
	}
	return h.e.SendKeys(ctx, cmd.SendKeys)
}

// Resume never retypes: the executor ends a restarted SendKeys
// UNKNOWN_OUTCOME without calling this (run.go); the belt stays shut if
// that ever changes, and the hub asks a person whether the keys landed.
func (h sendHandler) Resume(context.Context, exec.Request) *agentv1.CommandResult {
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_UNKNOWN_OUTCOME,
		Message: "SendKeys is never retyped after a restart; ask whether the keys landed",
	}
}

// interruptHandler is the exec.Handler for Interrupt. The wiring row
// registers one per server the agent serves, next to the session, window
// and send-keys handlers.
type interruptHandler struct {
	e *Ensurer
}

// InterruptHandler returns the Ensurer as the executor's Handler for
// Interrupt.
func (e *Ensurer) InterruptHandler() exec.Handler { return interruptHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h interruptHandler) Kind() string { return "Interrupt" }

// Capabilities are advertised in Hello while the handler is registered.
func (h interruptHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class re-runs the command from the start: C-c is idempotent in effect
// (02e), so a replayed command_id or a resumed Interrupt sends it again.
func (h interruptHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h interruptHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_Interrupt)
	if !ok || cmd.Interrupt == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: Interrupt handler got another kind",
		}
	}
	return h.e.Interrupt(ctx, cmd.Interrupt)
}

// Resume re-runs from the start: C-c is idempotent in effect, so there is
// nothing to continue, only to send again.
func (h interruptHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}

// respawnHandler is the exec.Handler for RespawnPane. The wiring row
// registers one per server the agent serves, next to the session, window,
// send-keys and interrupt handlers.
type respawnHandler struct {
	e *Ensurer
}

// RespawnHandler returns the Ensurer as the executor's Handler for
// RespawnPane.
func (e *Ensurer) RespawnHandler() exec.Handler { return respawnHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h respawnHandler) Kind() string { return "RespawnPane" }

// Capabilities are advertised in Hello while the handler is registered.
func (h respawnHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class re-runs the command from the start: re-running the pane's own
// command is idempotent in effect (02e), so a replayed command_id or a
// resumed RespawnPane respawns again.
func (h respawnHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h respawnHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_RespawnPane)
	if !ok || cmd.RespawnPane == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: RespawnPane handler got another kind",
		}
	}
	return h.e.RespawnPane(ctx, cmd.RespawnPane)
}

// Resume re-runs from the start: re-running the pane's own command is
// idempotent in effect, so there is nothing to continue, only to respawn
// again.
func (h respawnHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
