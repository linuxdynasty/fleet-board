package control

import (
	"context"
	"fmt"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Interrupt sends C-c to the pane: F4 (02-contracts, 02e "Repeat safe").
// The class is RepeatSafe — C-c is idempotent in effect, so a replayed or
// resumed command sends it again. There is no screen gate: the pane is
// interrupted precisely because a turn is running, so a ready prompt with
// an empty input line must never be required here (that gate is F3's).
// The pane-key and server-start checks are F3's, so C-c never lands on a
// reused pane id from an older server generation.
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
	// A fixed control key through the CLI, not operator text through the
	// observer client: building a typing.Line would need a new
	// typing.Purpose, which purpose.go allows only with an arbiter
	// ruling. The pane id is re-validated above and travels as its own
	// argv element, never a shell string, so no second tmux command can
	// smuggle in; the key itself is a constant. No typing.sent event is
	// emitted for the keystroke; the executor's per-kind command metrics
	// cover the command.
	if _, err := e.tmuxCall(ctx, "send-keys", "-t", paneID, "C-c"); err != nil {
		if strings.Contains(err.Error(), "can't find pane") {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
				"no pane "+paneID+" is on this server")
		}
		return e.down(err)
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("sent C-c to %s", paneID),
	}
}
