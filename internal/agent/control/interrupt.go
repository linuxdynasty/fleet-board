package control

import (
	"context"
	"errors"
	"fmt"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// Interrupt sends C-c to the pane: F4 (02-contracts, 02e "Repeat safe").
// The class is RepeatSafe — C-c is idempotent in effect, so a replayed or
// resumed command sends it again. There is no screen gate: the pane is
// interrupted precisely because a turn is running, so a ready prompt with
// an empty input line must never be required here (that gate is F3's).
// The pane-key and server-start checks are F3's, and the send itself goes
// through the observer client, which refuses a line built for an older
// server start: C-c never lands on a reused pane id from an older server
// generation, even when the server restarts between the checks and the
// send.
func (e *Ensurer) Interrupt(ctx context.Context, cmd *agentv1.Interrupt) *agentv1.CommandResult {
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
	// One pane's check-and-send is one critical section, shared with F3's
	// lock: the executor runs each command on its own goroutine, so
	// without this a C-c could land between F3's screen read and its
	// typing.
	unlock := e.lockPane(paneID)
	defer unlock()
	start, err := e.startID(ctx)
	if err != nil {
		return e.down(err)
	}
	if pane.GetServerStartId() != start {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
			"that pane belongs to an older server")
	}
	dead, _, res := e.paneInfo(ctx, paneID)
	if res != nil {
		return res
	}
	if dead {
		// F3 reads PANE_BUSY retryable here: its pane may become ready.
		// A dead pane never becomes interruptible by waiting (respawn
		// is F5's), so this refusal is not retryable and the hub moves
		// on instead of retrying into the deadline.
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"the pane is dead; nothing interrupted")
	}
	// A fixed control key through the observer client, never a one-shot
	// CLI argv: tmux.Client.Send checks the line's server start id against
	// the attached server's under one hold of its mutex, so a server that
	// stopped and restarted after the checks above — its pane ids restart
	// from zero — refuses with ErrWrongServer instead of taking the C-c
	// on the new generation's reused pane. The line borrows
	// PurposeOperatorText, which already sends C-c in F3; a new purpose
	// needs an arbiter ruling (purpose.go), and the gate a purpose names
	// is its caller's anyway. No typing.sent event is emitted for the
	// keystroke; the executor's per-kind command metrics cover the
	// command, so the borrowed purpose reaches no telemetry.
	line, err := typing.NewKeys(e.rec, pane, typing.PurposeOperatorText, typing.KeyCtrlC)
	if err != nil {
		// Unreachable: the pane key passed the shape checks above, and
		// the purpose and key are constants. Fail closed, never send.
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the interrupt line was refused")
	}
	if err := e.sender.Send(ctx, line); err != nil {
		switch {
		case errors.Is(err, tmux.ErrWrongServer):
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
				"that pane belongs to an older server")
		case errors.Is(err, tmux.ErrMaybeApplied):
			// The write may have reached tmux before the reply was
			// lost, so the C-c may already have landed.
			return unknown(fmt.Sprintf("sent C-c to %s, but the reply never came back; it may have landed", paneID))
		case errors.Is(err, tmux.ErrNotAttached):
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN,
				"the tmux server answers nothing")
		case errors.Is(err, tmux.ErrCommandFailed):
			// An explicit refusal wrote nothing: re-read the listing
			// to say whether the pane went away under the send.
			dead, _, res := e.paneInfo(ctx, paneID)
			if res != nil {
				return res
			}
			if dead {
				return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_BUSY,
					"the pane is dead; nothing interrupted")
			}
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"the tmux call failed")
		default:
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"the tmux call failed")
		}
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("sent C-c to %s", paneID),
	}
}
