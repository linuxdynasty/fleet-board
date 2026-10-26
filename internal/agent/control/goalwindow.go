package control

import (
	"context"
	"errors"
	"fmt"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// GoalWindowArgs are the fields a StartGoal window needs: the target
// session (nil or empty for a new session named SessionName), the
// window name, the working directory (already resolved and
// roots-checked), the harness, and the goal id carried in FLEET_GOAL_ID
// and the @fleet_goal_id window tag. A non-empty Profile is refused:
// per-harness profiles land in a later slice (ADR 0018).
type GoalWindowArgs struct {
	Target      *agentv1.SessionTarget
	SessionName string
	Window      string
	Dir         string
	Harness     string
	GoalID      string
	Profile     string
}

// GoalWindow is one opened goal window: the pane key, the session's
// name and fleet id, and whether this call created them (a resumed run
// reattaches to the window its goal tag names).
type GoalWindow struct {
	Pane           *typev1.PaneKey
	SessionName    string
	SessionID      string
	CreatedSession bool
	CreatedWindow  bool
}

// StartGoalWindow opens the goal window for StartGoal step ensure_window
// (G3): a new window in the target session (a new session when the
// target is empty) running the harness with FLEET_GOAL_ID, tagged with
// the goal id so a resumed run finds it instead of opening a second
// one. The binary comes from the probe's harness table and resolves on
// PATH; LookPath defaults to exec.LookPath.
func (e *Ensurer) StartGoalWindow(ctx context.Context, a GoalWindowArgs) (*GoalWindow, *agentv1.CommandResult) {
	if a.Profile != "" {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_HARNESS_PROFILE_MISSING,
			"no harness profile is configured on this machine")
	}
	if !validWindowName(a.Window) {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"window name must be 1-256 bytes with no control characters")
	}
	if !idRE.MatchString(a.GoalID) {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"goal id must match the 02e id format")
	}
	if a.Dir == "" || !e.underRoots(a.Dir) {
		return nil, refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the goal window starts under no allowed repo root")
	}
	binary, ok := probe.HarnessBinary(a.Harness)
	if !ok {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED,
			"this harness starts no goal window in this slice")
	}
	look := proc.LookPath
	if e.lookPath != nil {
		look = e.lookPath
	}
	path, err := look(binary)
	if err != nil || path == "" {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_INSTALLED,
			"harness "+binary+" is not on PATH")
	}
	if e.drain() {
		return nil, refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_DRAINING,
			"the machine is draining and starts no harness windows")
	}
	if sess, _, pane, ok := e.findGoalWindow(ctx, a.GoalID); ok {
		start, err := e.startID(ctx)
		if err != nil {
			return nil, e.down(err)
		}
		return &GoalWindow{
			Pane:        &typev1.PaneKey{Server: e.server, ServerStartId: start, PaneId: pane},
			SessionName: sess, SessionID: a.SessionName,
		}, nil
	}
	var sess string
	var createdSession bool
	if isEmptyTarget(a.Target) {
		if !nameRE.MatchString(a.SessionName) {
			return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"session name must match [a-z0-9][a-z0-9-]{0,31}")
		}
		found, err := e.hasSession(ctx, a.SessionName)
		if err != nil {
			return nil, e.down(err)
		}
		if !found {
			if err := e.createGoalSession(ctx, a, path); err != nil {
				return nil, e.down(err)
			}
			createdSession = true
		}
		sess = a.SessionName
	} else {
		s, bad := e.resolveSession(ctx, a.Target)
		if bad != nil {
			return nil, bad
		}
		sess = s
	}
	_, pane, bad := e.createGoalWindow(ctx, sess, a, path)
	if bad != nil {
		return nil, bad
	}
	start, err := e.startID(ctx)
	if err != nil {
		return nil, e.down(err)
	}
	return &GoalWindow{
		Pane:        &typev1.PaneKey{Server: e.server, ServerStartId: start, PaneId: pane},
		SessionName: sess, SessionID: a.SessionName,
		CreatedSession: createdSession, CreatedWindow: true,
	}, nil
}

// isEmptyTarget reports whether the StartGoal command named no session:
// a new session carries the goal window.
func isEmptyTarget(t *agentv1.SessionTarget) bool {
	if t == nil {
		return true
	}
	switch by := t.GetBy().(type) {
	case *agentv1.SessionTarget_Name:
		return by.Name == ""
	case *agentv1.SessionTarget_SessionId:
		return by.SessionId.GetValue() == ""
	case *agentv1.SessionTarget_TmuxId:
		return by.TmuxId == ""
	default:
		return true
	}
}

// createGoalSession starts the goal session with the harness as its
// first command: new-session -d -s name -c dir -e FLEET_GOAL_ID binary,
// then the session tags. The window it implicitly creates is the goal
// window only when its name matches; otherwise createGoalWindow opens
// the named one next.
func (e *Ensurer) createGoalSession(ctx context.Context, a GoalWindowArgs, binary string) error {
	args := []string{
		"new-session", "-d", "-s", a.SessionName, "-c", a.Dir,
		"-e", "FLEET_GOAL_ID=" + a.GoalID, binary,
	}
	out, err := e.tmuxCall(ctx, args...)
	if err != nil {
		return err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 0 && len(f) != 1 {
		return fmt.Errorf("tmux new-session: unexpected output %q", out)
	}
	return e.tag(ctx, a.SessionName, "", a.SessionName, "", a.GoalID)
}

// createGoalWindow opens the named window in sess running the harness
// with FLEET_GOAL_ID, capped like F2, then tags it with the goal id for
// idempotent resume. A window the goal tag already names is reused.
func (e *Ensurer) createGoalWindow(ctx context.Context, sess string, a GoalWindowArgs, binary string) (tmuxWindow, string, *agentv1.CommandResult) {
	var empty tmuxWindow
	e.mu.Lock()
	defer e.mu.Unlock()
	windows, err := e.listWindows(ctx, sess)
	if err != nil {
		return empty, "", e.down(err)
	}
	for _, w := range windows {
		stored, err := e.windowTag(ctx, w.id, "@fleet_goal_id")
		if err != nil {
			return empty, "", e.down(err)
		}
		if stored == a.GoalID {
			pane, res := e.windowPane(ctx, w)
			if res != nil {
				return empty, "", res
			}
			return w, pane, nil
		}
	}
	if w, ok := findWindow(windows, a.Window); ok {
		return empty, "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"window "+w.name+" exists and carries another goal")
	}
	if len(windows) >= int(e.maxWindows()) {
		return empty, "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_MAX_WINDOWS_REACHED,
			"session "+sess+" already holds its window cap")
	}
	args := []string{
		"new-window", "-d", "-t", sess, "-n", a.Window, "-c", a.Dir,
		"-e", "FLEET_GOAL_ID=" + a.GoalID, binary, "-P", "-F", windowFormat,
	}
	out, err := e.tmuxCall(ctx, args...)
	if err != nil {
		return empty, "", e.down(err)
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 3 {
		return empty, "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the window create answered an unreadable row")
	}
	win := tmuxWindow{id: f[0], name: a.Window}
	if err := e.tagWindow(ctx, win.id, f[1], a.GoalID, a.SessionName, ""); err != nil {
		_, _ = e.tmuxCall(ctx, "kill-window", "-t", win.id)
		return empty, "", e.down(err)
	}
	return win, f[1], nil
}

// windowPane reads a window's first pane id.
func (e *Ensurer) windowPane(ctx context.Context, w tmuxWindow) (string, *agentv1.CommandResult) {
	out, err := e.tmuxCall(ctx, "list-panes", "-t", w.id, "-F", "#{pane_id}")
	if err != nil {
		return "", e.down(err)
	}
	pane := strings.Fields(strings.TrimSpace(out))
	if len(pane) == 0 {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"window "+w.name+" has no panes left")
	}
	return pane[0], nil
}

// findGoalWindow returns the session, window and pane carrying the goal
// tag, for idempotent resume after a lost result.
func (e *Ensurer) findGoalWindow(ctx context.Context, goalID string) (sess string, win tmuxWindow, pane string, ok bool) {
	out, err := e.tmuxCall(ctx, "list-sessions", "-F", "#{session_name}")
	if err != nil {
		return "", tmuxWindow{}, "", false
	}
	for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
		if name == "" {
			continue
		}
		windows, err := e.listWindows(ctx, name)
		if err != nil {
			continue
		}
		for _, w := range windows {
			stored, err := e.windowTag(ctx, w.id, "@fleet_goal_id")
			if err != nil || stored != goalID {
				continue
			}
			p, res := e.windowPane(ctx, w)
			if res != nil {
				continue
			}
			return name, w, p, true
		}
	}
	return "", tmuxWindow{}, "", false
}

// StartPipeCapture starts pipe-pane capture of the goal pane into the
// agent's raw file (K1, StartGoal step start_log_capture): plain append
// bytes through cat, so the capture holds what the pane showed. The
// file must be absolute; tmux runs the command, never the agent shell,
// and the single quotes around the path are escaped, so a crafted path
// cannot smuggle a second command. Stopping and shipping are row 25's.
func (e *Ensurer) StartPipeCapture(ctx context.Context, paneID, file string) error {
	if !paneIDRE.MatchString(paneID) {
		return errors.New("control: a pane id looks like %3")
	}
	if file == "" || file[0] != '/' || strings.ContainsAny(file, "\n\x00") {
		return errors.New("control: the capture file is an absolute path without newlines")
	}
	quoted := "'" + strings.ReplaceAll(file, "'", `'\''`) + "'"
	_, err := e.tmuxCall(ctx, "pipe-pane", "-o", "-t", paneID, "cat >> "+quoted)
	return err
}
