package control

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// windowIDRE is the "@<digits>" shape of a tmux window id (ids.proto
// WindowKey). Anything else as a window_id is a malformed command, not
// an address: passing target expressions to -t could operate on another
// window.
var windowIDRE = regexp.MustCompile(`^@[0-9]{1,10}$`)

// KillWindow kills the named window: F6 (02-contracts, 02e "Repeat
// safe"). The class is RepeatSafe — killing an already-gone window is
// refused WINDOW_NOT_FOUND, and a replayed command_id never reaches here
// (the envelope answers it from the log), so a resume re-runs the kill
// from the start. This slice kills by tmux window id only: there is no
// screen gate (a window is killed with live work in it — that is the
// command), no drain refusal (cleanup is what a draining machine does),
// and no last-window refusal (the hub kills single-window fleet sessions
// as their cleanup; the role matrix, not this handler, keeps the
// developer's windows for the owner).
//
// Default deny: the window key's shape, its server, and its generation
// are checked first, then the window is located by id. A stale start id
// or a window the listing does not know reads WINDOW_NOT_FOUND, never a
// kill at a reused id. fleetOnly is the executor's 02g F cell (the
// scheduler's row): only it can see tmux, so it refuses a window the
// fleet's tags do not mark with ROLE_INSUFFICIENT before any kill. The
// same re-check covers every non-owner role (role is the command's own
// requested_by, already admitted by the executor): a tag removed between
// the role check and the kill — across a local-approval wait, say —
// reads ROLE_INSUFFICIENT instead of killing under the wrong row. Only
// an owner kills an unmarked window. A refused kill re-reads the
// listing to say whether the window went away under the call
// (WINDOW_NOT_FOUND) or tmux refused another way (INTERNAL); no message
// matches tmux's error text, so no tmux version can skew it.
func (e *Ensurer) KillWindow(ctx context.Context, cmd *agentv1.KillWindow, fleetOnly bool, role typev1.Role) *agentv1.CommandResult {
	win := cmd.GetWindow()
	if win == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a window key is required")
	}
	if win.GetServer() != e.server {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"this ensurer does not serve that tmux server")
	}
	winID := win.GetWindowId()
	if !windowIDRE.MatchString(winID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a window id looks like @3")
	}
	if win.GetServerStartId() == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a server start id is required")
	}
	// One server's check-and-kill is one critical section, shared with
	// F2's lock: the executor runs each command on its own goroutine,
	// so without this a kill could land between F2's listing and its
	// create. Never nested: nothing under it calls back into the
	// Ensurer.
	e.mu.Lock()
	defer e.mu.Unlock()
	start, err := e.startID(ctx)
	if err != nil {
		return e.down(err)
	}
	if win.GetServerStartId() != start {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND,
			"that window belongs to an older server")
	}
	sess, res := e.windowSession(ctx, winID)
	if res != nil {
		return res
	}
	if sess == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND,
			"no window "+winID+" is on this server")
	}
	if !e.fleetMarked(ctx, winID, sess) && (fleetOnly || role != typev1.Role_ROLE_OWNER) {
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
			"that window is not the fleet's")
	}
	if _, err := e.tmuxCall(ctx, "kill-window", "-t", winID); err != nil {
		// A refused kill wrote nothing the listing can see: re-read
		// it to say whether the window went away under the call.
		sess, res := e.windowSession(ctx, winID)
		if res != nil {
			return res
		}
		if sess == "" {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WINDOW_NOT_FOUND,
				"window "+winID+" went away under the kill")
		}
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"tmux refused the kill")
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("killed window %s", winID),
	}
}

// windowSession names the session holding winID, or "" when no window
// of that id is on this server. The name is the line's remainder after
// the id: session names may hold spaces, so only the first field splits.
func (e *Ensurer) windowSession(ctx context.Context, winID string) (string, *agentv1.CommandResult) {
	out, err := e.tmuxCall(ctx, "list-windows", "-a", "-F", "#{window_id} #{session_name}")
	if err != nil {
		return "", e.down(err)
	}
	for line := range strings.Lines(strings.TrimSpace(out)) {
		id, name, _ := strings.Cut(line, " ")
		if id == winID {
			return name, nil
		}
	}
	return "", nil
}

// fleetMarked reports whether winID in sess is the fleet's own: its own
// @fleet_session_id window option (F2's tag) or its session's (F1's tag,
// which the session's first window carries without a window tag of its
// own). The tag must keep the 02e id shape F1 writes; anything else is
// not a fleet mark. Unknown is false: the caller fails closed.
func (e *Ensurer) fleetMarked(ctx context.Context, winID, sess string) bool {
	if tag, err := e.windowTag(ctx, winID, "@fleet_session_id"); err == nil && idRE.MatchString(tag) {
		return true
	}
	if tag, err := e.sessionTag(ctx, sess, "@fleet_session_id"); err == nil && idRE.MatchString(tag) {
		return true
	}
	return false
}

// fleetWindow places the KillWindow target for the role matrix (02g):
// true reads the fleet_window row, anything else the developer_window
// row, the strictest one. Only the tags place a window, so a bad shape,
// a stale generation, a window the listing does not know, or a tmux the
// lookup cannot reach all read false — the developer's row — and Run
// refuses them with their own reasons.
func (e *Ensurer) fleetWindow(ctx context.Context, cmd *agentv1.Command) bool {
	win := cmd.GetKillWindow().GetWindow()
	if win == nil || win.GetServer() != e.server ||
		!windowIDRE.MatchString(win.GetWindowId()) || win.GetServerStartId() == "" {
		return false
	}
	start, err := e.startID(ctx)
	if err != nil || start != win.GetServerStartId() {
		return false
	}
	sess, res := e.windowSession(ctx, win.GetWindowId())
	if res != nil || sess == "" {
		return false
	}
	return e.fleetMarked(ctx, win.GetWindowId(), sess)
}

// killHandler is the exec.Handler for KillWindow. The wiring row
// registers one per server the agent serves, next to the session,
// window, send-keys, interrupt and respawn handlers.
type killHandler struct {
	e *Ensurer
}

// KillWindowHandler returns the Ensurer as the executor's Handler for
// KillWindow.
func (e *Ensurer) KillWindowHandler() exec.Handler { return killHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h killHandler) Kind() string { return "KillWindow" }

// Capabilities are advertised in Hello while the handler is registered.
func (h killHandler) Capabilities() []*typev1.Capability {
	return capabilitiesFor(h.e.kind)
}

// Class re-runs the command from the start: killing is idempotent in
// effect for a window still there (02e), so a replayed command_id or a
// resumed KillWindow kills again.
func (h killHandler) Class(*agentv1.Command) exec.Class { return exec.RepeatSafe }

// Variant answers the role-matrix row (02g): "fleet_window" only for a
// window the fleet's tags mark, "developer_window" for anything else.
// It reads tmux on the executor's context, like Run does; a cancelled
// lookup places nothing.
func (h killHandler) Variant(ctx context.Context, c *agentv1.Command) string {
	if h.e.fleetWindow(ctx, c) {
		return "fleet_window"
	}
	return "developer_window"
}

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h killHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_KillWindow)
	if !ok || cmd.KillWindow == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: KillWindow handler got another kind",
		}
	}
	return h.e.KillWindow(ctx, cmd.KillWindow, r.FleetOnly, r.Command.GetRequestedBy().GetRole())
}

// Resume re-runs from the start: killing is idempotent in effect for a
// window still there, so there is nothing to continue, only to kill
// again.
func (h killHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	return h.Run(ctx, r)
}
