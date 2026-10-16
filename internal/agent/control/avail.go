package control

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
)

// Marks records one pane's availability mark on the observer's overlay
// (inventory.Observer.ApplyAvailability). *inventory.Observer satisfies it;
// tests use a stub. Required: SetPaneAvailability never marks without it.
type Marks interface {
	ApplyAvailability(pane *typev1.PaneKey, avail *typev1.Availability, reason typev1.AvailabilityReason) error
}

// Compile-time proof the production observer satisfies the seam.
var _ Marks = (*inventory.Observer)(nil)

// SetAvailability records the developer's board mark for one pane: E11
// (02h "Availability per pane"). The class is RepeatSafe: a mark is
// declarative and last-write-wins, so a replayed command_id or a resume
// sets the same mark again. The mark itself is pure memory on the
// observer's overlay — no tmux call, no screen gate, no pane lock (marks
// never type, so there is nothing to serialize against F3's check-and-send;
// the overlay's own mutex serializes concurrent marks and its reconcile
// emits the AvailabilityChanged edge on the loop's next read). A mark for
// a pane the last read does not know, an unknown server, or a stale
// server start reads PANE_NOT_FOUND, so a reused pane id never inherits a
// mark; the hub re-reads the Snapshot and re-sends.
func (e *Ensurer) SetAvailability(_ context.Context, cmd *agentv1.SetPaneAvailability) *agentv1.CommandResult {
	if cmd == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"SetPaneAvailability is required")
	}
	pane := cmd.GetPane()
	if pane == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a pane key is required")
	}
	if pane.GetServer() != e.server {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"this ensurer does not serve that tmux server")
	}
	paneID := pane.GetPaneId()
	if !paneIDRE.MatchString(paneID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a pane id looks like %3")
	}
	if pane.GetServerStartId() == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a server start id is required")
	}
	reason := cmd.GetReason()
	switch reason {
	case typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD,
		typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD,
		typev1.AvailabilityReason_AVAILABILITY_REASON_GOAL_MERGED:
	case typev1.AvailabilityReason_AVAILABILITY_REASON_UNSPECIFIED:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"an availability reason is required")
	default:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"that reason is reported by the agent, never commanded")
	}
	// Fresh-context is the owner's mark-time choice (command_tmux.proto):
	// kept only while the pane is marked available for work. A clear
	// carries no context choice, and a merged goal's pane may always be
	// cleared, so both drop it.
	fresh := cmd.GetAvailableForWork() &&
		reason == typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD &&
		cmd.GetFreshContextOnReuse()
	if err := e.marks.ApplyAvailability(pane,
		&typev1.Availability{AvailableForWork: cmd.GetAvailableForWork(), FreshContextOnReuse: fresh},
		reason); err != nil {
		// The only reachable failures are liveness: a pane the last
		// read does not know, or a start id from an older server
		// generation. Either way the pane is not on this server at
		// that start id, and retrying the same key never lands it.
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
			"that pane is not on this server at that start id")
	}
	if cmd.GetAvailableForWork() {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_OK,
			Message: "marked " + paneID + " available for work",
		}
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: "cleared " + paneID + " availability",
	}
}

// availHandler is the exec.Handler for SetPaneAvailability. The wiring row
// registers one per server the agent serves, next to the session, window,
// send-keys and interrupt handlers.
type availHandler struct {
	e *Ensurer
}

// AvailabilityHandler returns the Ensurer as the executor's Handler for
// SetPaneAvailability.
func (e *Ensurer) AvailabilityHandler() exec.Handler { return availHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h availHandler) Kind() string { return "SetPaneAvailability" }

// Capabilities are advertised in Hello while the handler is registered.
func (h availHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class re-runs the command from the start: a mark is declarative and
// last-write-wins, so a replayed command_id sets the same mark again.
func (h availHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h availHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_SetPaneAvailability)
	if !ok || cmd.SetPaneAvailability == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: SetPaneAvailability handler got another kind",
		}
	}
	return h.e.SetAvailability(ctx, cmd.SetPaneAvailability)
}

// Resume re-runs from the start: a mark is declarative, so there is
// nothing to continue, only to record again.
func (h availHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
