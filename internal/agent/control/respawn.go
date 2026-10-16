package control

import (
	"context"
	"fmt"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// RespawnPane restarts a dead pane: F5 (02-contracts, 02e "Repeat safe").
// The class is RepeatSafe — re-running the pane's own command is
// idempotent in effect, so a replayed or resumed command respawns again.
// This slice respawns the pane's own command only: any harness, resume,
// profile or harness conversation field is refused until the adapter
// start surface lands in a later slice of row 18 (the F1/F2 rule).
// A live pane is refused PANE_BUSY, not retryable: respawn would kill
// live work, so there is no destructive default and the hub re-evaluates
// instead of retrying a kill into the deadline. The pane-key and
// server-start checks are F3's, held under that pane's lock shared with
// F3 and F4, so two commands to one pane cannot interleave a check with
// another's send. The respawn itself is a plain one-shot
// `respawn-pane -t` with no -k flag and no command: tmux re-runs what the
// pane ran, and tmux itself refuses a pane that is still active, so even
// a pane that came alive between the listing and the call is never
// killed. A refused respawn re-reads the listing to say whether the pane
// went away under the call (PANE_NOT_FOUND), came alive (PANE_BUSY) or
// tmux refused another way (INTERNAL); no message matches tmux's error
// text, so no tmux version can skew it. Residual race: a server restart
// between the start-id check and the call could address a reused pane
// id — the missing -k keeps a live reuse safe, and the start-id check
// keeps the window to a restart racing the call. Follow-up, not
// silently dropped: the 01f drain rule for F5 (refused while draining
// except a pane with a registered live goal) needs the goal registry,
// which this package cannot see yet.
func (e *Ensurer) RespawnPane(ctx context.Context, cmd *agentv1.RespawnPane) *agentv1.CommandResult {
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
	if cmd.GetHarness().GetName() != "" || cmd.GetResume() {
		return refuse(typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED,
			"this agent build respawns the pane's own command only; harness restart lands in a later slice")
	}
	if cmd.GetProfile() != "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness profile without a harness means nothing")
	}
	if cmd.GetHarnessSessionId() != "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness conversation without a harness means nothing")
	}
	// One pane's check-and-respawn is one critical section, shared with
	// F3's and F4's lock: the executor runs each command on its own
	// goroutine, so without this a respawn could land between F3's
	// screen read and its typing.
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
	if !dead {
		// A live pane never becomes respawnable by waiting (only its
		// death does, which the hub sees in the snapshot), so this
		// refusal is not retryable and the hub moves on instead of
		// retrying a kill into the deadline.
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"the pane is alive; respawn never kills live work")
	}
	if _, err := e.tmuxCall(ctx, "respawn-pane", "-t", paneID); err != nil {
		// A refused respawn wrote nothing (and without -k never
		// killed): re-read the listing to say what the pane became
		// under the call.
		dead, _, res := e.paneInfo(ctx, paneID)
		if res != nil {
			return res
		}
		if !dead {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_BUSY,
				"the pane came alive under the respawn; nothing respawned")
		}
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"tmux refused the respawn")
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("respawned %s", paneID),
	}
}
