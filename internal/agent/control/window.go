package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// defaultMaxWindows caps the windows per session when Options.MaxWindows is
// nil: the settings default (03e2), so an unconfigured Ensurer still refuses
// before tmux grows a session without bound.
const defaultMaxWindows = 12

// maxWindows is the window cap for the target session. Zero refuses every
// create; reuse of an existing window still attaches.
func (e *Ensurer) maxWindows() uint32 {
	if e.maxwin == nil {
		return defaultMaxWindows
	}
	return e.maxwin()
}

// EnsureWindow creates the named window in the resolved session or, with
// reuse, attaches to the one already there. This slice starts a login shell
// only: a harness to start, or resume of a harness conversation, is refused
// until the adapter start surface lands in a later slice of row 18.
func (e *Ensurer) EnsureWindow(ctx context.Context, cmd *agentv1.EnsureWindow) *agentv1.CommandResult {
	name := cmd.GetWindowName()
	if name == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"window name must not be empty")
	}
	t := cmd.GetSession()
	if t == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a session target is required")
	}
	if k := t.GetServer(); k != typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED && k != e.kind {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"this ensurer does not serve that tmux server")
	}
	harness := cmd.GetHarness().GetName()
	starting := harness != "" || cmd.GetResume()
	if starting && e.drain() {
		// 01f M4: while draining, harness-starting F2 is refused. A
		// login shell is not a harness start and runs on.
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_DRAINING,
			"the machine is draining and starts no harness windows")
	}
	if starting {
		return refuse(typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED,
			"this agent build starts login shells only; harness start lands in a later slice")
	}
	if cmd.GetProfile() != "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness profile without a harness means nothing")
	}
	if cmd.GetHarnessSessionId() != "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness conversation without a harness means nothing")
	}
	goalID := cmd.GetGoalId().GetValue()
	if goalID != "" && !idRE.MatchString(goalID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"goal id must match the 02e id format")
	}
	dir, bad := e.resolveDir(cmd.GetWorkdir())
	if bad != nil {
		return bad
	}

	sess, res := e.resolveSession(ctx, t)
	if res != nil {
		return res
	}
	found, err := e.hasSession(ctx, sess)
	if err != nil {
		return e.down(err)
	}
	if !found {
		// F2 never creates sessions: a missing one is the hub's stale
		// view, not a build order.
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"session "+sess+" is not on this server")
	}
	windows, err := e.listWindows(ctx, sess)
	if err != nil {
		return e.down(err)
	}
	if w, ok := findWindow(windows, name); ok {
		return e.existingWindow(ctx, w, cmd.GetReuse(), goalID)
	}
	if len(windows) >= int(e.maxWindows()) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_MAX_WINDOWS_REACHED,
			"session "+sess+" already holds its window cap")
	}
	win, pane, err := e.createWindow(ctx, windowArgs{
		session: sess, name: name, dir: dir, goalID: goalID,
	})
	if err != nil {
		return e.down(err)
	}
	return e.describeWindow(ctx, win, pane, true)
}

// resolveDir maps the command's workdir to a local checkout path under an
// allowed repo root: the F1 rule, shared by F2. A ".." element is refused
// outright, never clamped into the checkout.
func (e *Ensurer) resolveDir(workdir *typev1.Workdir) (string, *agentv1.CommandResult) {
	dir, err := e.workdir(workdir)
	if err != nil || dir == "" {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
			"the workdir resolves to no local checkout")
	}
	if rel := workdir.GetRelativePath(); rel != "" {
		// 01e: the agent rejects "..", it never clamps it into the
		// checkout: "../secret" must not quietly become "<repo>/secret".
		// The raw path is checked: cleaning first would resolve the
		// traversal away before it is seen.
		for _, el := range strings.Split(rel, "/") {
			if el == ".." {
				return "", refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
					"the work path climbs out of its checkout")
			}
		}
		dir = filepath.Join(dir, filepath.Clean("/"+rel))
	}
	if !e.underRoots(dir) {
		return "", refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the workdir is under no allowed repo root")
	}
	return dir, nil
}

// resolveSession maps a SessionTarget to the tmux target of a session on
// this server: a fleet name, the hub session id stored on it, or the tmux
// id guarded by the server start id (tmux reuses ids after a restart).
func (e *Ensurer) resolveSession(ctx context.Context, t *agentv1.SessionTarget) (string, *agentv1.CommandResult) {
	switch by := t.GetBy().(type) {
	case *agentv1.SessionTarget_Name:
		if !nameRE.MatchString(by.Name) {
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"session name must match [a-z0-9][a-z0-9-]{0,31}")
		}
		return by.Name, nil
	case *agentv1.SessionTarget_SessionId:
		id := by.SessionId.GetValue()
		if !idRE.MatchString(id) {
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session id must match the 02e id format")
		}
		name, err := e.findSessionByID(ctx, id)
		if err != nil {
			return "", e.down(err)
		}
		if name == "" {
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
				"no session on this server carries that session id")
		}
		return name, nil
	case *agentv1.SessionTarget_TmuxId:
		if by.TmuxId == "" || t.GetServerStartId() == "" {
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
				"a tmux id needs the server start id it belongs to")
		}
		start, err := e.startID(ctx)
		if err != nil {
			return "", e.down(err)
		}
		if start != t.GetServerStartId() {
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
				"that tmux id belongs to an older server")
		}
		return by.TmuxId, nil
	default:
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a session target names a session by name, session id or tmux id")
	}
}

// findSessionByID returns the name of the session whose @fleet_session_id
// tag carries id, or "" when none does.
func (e *Ensurer) findSessionByID(ctx context.Context, id string) (string, error) {
	out, err := e.tmuxCall(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return "", err
	}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "" {
			continue
		}
		stored, err := e.sessionTag(ctx, name, "@fleet_session_id")
		if err != nil {
			return "", err
		}
		if stored == id {
			return name, nil
		}
	}
	return "", nil
}

// tmuxWindow is one row of list-windows: the window id tmux targets, its
// index in the session, and its name as shown.
type tmuxWindow struct {
	id    string
	index uint32
	name  string
}

// listWindows names every window of the session. The name is the line's
// remainder after the id and index: window names may hold spaces, so only
// the first two fields split.
func (e *Ensurer) listWindows(ctx context.Context, sess string) ([]tmuxWindow, error) {
	out, err := e.tmuxCall(ctx, "list-windows", "-t", sess, "-F", "#{window_id} #{window_index} #{window_name}")
	if err != nil {
		return nil, err
	}
	var windows []tmuxWindow
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		w, ok := parseWindowLine(line)
		if !ok {
			return nil, fmt.Errorf("tmux list-windows: unexpected row %q", line)
		}
		windows = append(windows, w)
	}
	return windows, nil
}

// parseWindowLine splits one "#{window_id} #{window_index} #{window_name}"
// row. Window ids hold no spaces and indexes are numeric; everything past
// the second space is the name, spacing intact.
func parseWindowLine(line string) (tmuxWindow, bool) {
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return tmuxWindow{}, false
	}
	rest := line[i+1:]
	j := strings.IndexByte(rest, ' ')
	fields, name := rest, ""
	if j >= 0 {
		fields, name = rest[:j], rest[j+1:]
	}
	n, err := strconv.ParseUint(fields, 10, 32)
	if err != nil {
		return tmuxWindow{}, false
	}
	return tmuxWindow{id: line[:i], index: uint32(n), name: name}, true
}

// findWindow returns the first window carrying name. tmux allows duplicate
// window names, so two racing creates can both land: the first listed wins,
// and a later slice with a window identity (FLEET_PANE_KEY) tells them apart.
func findWindow(windows []tmuxWindow, name string) (tmuxWindow, bool) {
	for _, w := range windows {
		if w.name == name {
			return w, true
		}
	}
	return tmuxWindow{}, false
}

// existingWindow answers for a window that is already there. With reuse it
// attaches; without, only a stored goal tag matching the requested goal id
// completes the call: that is this command's own effect seen again after
// its result was lost (F2 is idempotent, 01f), while any other window under
// the name stays a conflict.
func (e *Ensurer) existingWindow(ctx context.Context, w tmuxWindow, reuse bool, goalID string) *agentv1.CommandResult {
	if !reuse {
		if goalID == "" {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"window "+w.name+" exists and reuse is false")
		}
		stored, err := e.windowTag(ctx, w.id, "@fleet_goal_id")
		if err != nil {
			return e.down(err)
		}
		if stored != goalID {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"window "+w.name+" exists and reuse is false")
		}
	}
	return e.describeWindow(ctx, w, "", false)
}

// windowArgs are the fields a login-shell window create needs. Session and
// owner come from the session's stored tags, read at create time: a window
// in a fleet session reports as the fleet's, in the developer's own as the
// developer's.
type windowArgs struct {
	session string
	name    string
	dir     string
	goalID  string
}

// createWindow runs new-window detached in dir, with the FLEET_* environment
// the new shell carries from its first process. Every argument is a separate
// element, never a shell string; -e needs tmux 3.2 or newer (the minimum the
// fleet supports).
func (e *Ensurer) createWindow(ctx context.Context, a windowArgs) (tmuxWindow, string, error) {
	var empty tmuxWindow
	sessionID, err := e.sessionTag(ctx, a.session, "@fleet_session_id")
	if err != nil {
		return empty, "", err
	}
	owner, err := e.sessionTag(ctx, a.session, "@fleet_owner")
	if err != nil {
		return empty, "", err
	}
	args := []string{"new-window", "-d", "-t", a.session, "-n", a.name, "-c", a.dir}
	if sessionID != "" {
		args = append(args, "-e", "FLEET_SESSION_ID="+sessionID)
	}
	if owner != "" {
		args = append(args, "-e", "FLEET_OWNER="+owner)
	}
	if a.goalID != "" {
		args = append(args, "-e", "FLEET_GOAL_ID="+a.goalID)
	}
	args = append(args, "-P", "-F", windowFormat)
	out, err := e.tmuxCall(ctx, args...)
	if err != nil {
		return empty, "", err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 3 {
		return empty, "", fmt.Errorf("tmux new-window: unexpected -P output %q", out)
	}
	win := tmuxWindow{id: f[0], name: a.name}
	n, err := strconv.ParseUint(f[2], 10, 32)
	if err != nil {
		return empty, "", fmt.Errorf("tmux new-window: unexpected -P output %q", out)
	}
	win.index = uint32(n)
	if err := e.tagWindow(ctx, win.id, f[1], a.goalID, sessionID, owner); err != nil {
		return empty, "", err
	}
	return win, f[1], nil
}

// windowFormat prints the new window id, its first pane id and its index on
// one line. Ids hold no spaces; the index is numeric.
const windowFormat = "#{window_id} #{pane_id} #{window_index}"

// tagWindow sets the @fleet_* window options the observer reads, so the new
// window is attributed whatever the observer reads first, and turns
// automatic-rename off for the fleet's own window only (01f). The pane key
// is server/server_start_id/pane_id, the same rule as F1. Tag failures are
// not fatal: the window exists and answers, and only its attribution
// degrades.
func (e *Ensurer) tagWindow(ctx context.Context, winID, paneID, goalID, sessionID, owner string) error {
	start, err := e.startID(ctx)
	if err != nil {
		return err
	}
	key := e.server + "/" + start + "/" + paneID
	var errs []string
	set := func(args ...string) {
		if _, err := e.tmuxCall(ctx, args...); err != nil {
			errs = append(errs, err.Error())
		}
	}
	set("set-option", "-w", "-t", winID, "@fleet_pane_key", key)
	if sessionID != "" {
		set("set-option", "-w", "-t", winID, "@fleet_session_id", sessionID)
	}
	if owner != "" {
		set("set-option", "-w", "-t", winID, "@fleet_owner", owner)
	}
	if goalID != "" {
		set("set-option", "-w", "-t", winID, "@fleet_goal_id", goalID)
	}
	set("set-option", "-w", "-t", winID, "automatic-rename", "off")
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// windowTag reads one @fleet_* window option. An option that was never set
// reads empty; a server that answers nothing is a failure.
func (e *Ensurer) windowTag(ctx context.Context, winID, option string) (string, error) {
	out, err := e.tmuxCall(ctx, "show-options", "-w", "-v", "-t", winID, option)
	if err != nil {
		if isDown(err.Error()) {
			return "", err
		}
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// describeWindow builds the OK result for the window: its key, index and
// shown name, the first pane's key, and whether this call created it. The
// goal tag comes from the window's stored options, never from the request.
func (e *Ensurer) describeWindow(ctx context.Context, w tmuxWindow, paneID string, created bool) *agentv1.CommandResult {
	if paneID == "" {
		out, err := e.tmuxCall(ctx, "list-panes", "-t", w.id, "-F", "#{pane_id}")
		if err != nil {
			return e.down(err)
		}
		paneID = strings.Fields(strings.TrimSpace(out))[0]
		if paneID == "" {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
				"window "+w.name+" has no panes left")
		}
	}
	start, err := e.startID(ctx)
	if err != nil {
		return e.down(err)
	}
	verb := "reused window "
	if created {
		verb = "created window "
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Reason:  typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: verb + w.name,
		Detail: &agentv1.CommandResult_EnsureWindow{EnsureWindow: &agentv1.EnsureWindowResult{
			Window: &agentv1.TmuxWindow{
				Key:   &typev1.WindowKey{Server: e.server, ServerStartId: start, WindowId: w.id},
				Index: w.index,
				Name:  cap256(w.name),
			},
			Created: created,
			Pane: &typev1.PaneKey{
				Server:        e.server,
				ServerStartId: start,
				PaneId:        paneID,
			},
		}},
	}
}
