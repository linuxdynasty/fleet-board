package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// tmuxCall runs one tmux CLI call on the bound server and returns its
// stdout. The binary and socket are fixed per Ensurer; every argument after
// them is a separate element, never a shell string.
func (e *Ensurer) tmuxCall(ctx context.Context, args ...string) (string, error) {
	argv := append([]string{"-S", e.socket}, args...)
	res, err := e.runner.Run(ctx, proc.Spec{Caller: e.caller, Path: e.tmux, Args: argv})
	if err != nil {
		out := strings.TrimSpace(string(res.Stderr))
		if out == "" {
			out = strings.TrimSpace(string(res.Stdout))
		}
		if out != "" {
			return "", fmt.Errorf("tmux %s: %s: %w", strings.Join(args, " "), out, err)
		}
		return "", fmt.Errorf("tmux %s: %w", strings.Join(args, " "), err)
	}
	return string(res.Stdout), nil
}

// hasSession reports whether the named session exists. has-session prints
// nothing on success; the tmux id comes from describe's list-panes read.
func (e *Ensurer) hasSession(ctx context.Context, name string) (bool, error) {
	_, err := e.tmuxCall(ctx, "has-session", "-t", name)
	if err != nil {
		if strings.Contains(err.Error(), "can't find session") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// createArgs are the fields a login-shell create needs.
type createArgs struct {
	name      string
	dir       string
	window    string
	sessionID string
	owner     string
	goalID    string
}

// create runs new-session detached in dir, with the FLEET_* environment the
// observer reads. -e sets the variables before the first process forks, so
// the login shell itself carries them; the @fleet_* session options (set by
// tag) carry them for the observer's preferred read.
func (e *Ensurer) create(ctx context.Context, a createArgs) error {
	args := []string{"new-session", "-d", "-s", a.name, "-c", a.dir}
	if w := cap256(a.window); w != "" {
		args = append(args, "-n", w)
	}
	for k, v := range fleetEnv(a) {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, "-P", "-F", createFormat)
	out, err := e.tmuxCall(ctx, args...)
	if err != nil {
		return err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 3 {
		return fmt.Errorf("tmux new-session: unexpected -P output %q", out)
	}
	return e.tag(ctx, a.name, f[1], a.sessionID, a.owner, a.goalID)
}

// createFormat prints the new session, pane and window ids on one line.
const createFormat = "#{session_id} #{pane_id} #{window_id}"

// cap256 cuts a tmux display name to the 256-byte cap of snapshot.proto,
// on a rune boundary like the observer's reads.
func cap256(s string) string {
	if len(s) <= 256 {
		return s
	}
	cut := 0
	for i := range s {
		if i > 256 {
			break
		}
		cut = i
	}
	return s[:cut]
}

// fleetEnv are the FLEET_* session variables for a new session. Empty ids
// stay unset: no variable is better than a shapeless one.
func fleetEnv(a createArgs) map[string]string {
	env := map[string]string{}
	if a.sessionID != "" {
		env["FLEET_SESSION_ID"] = a.sessionID
	}
	if a.owner != "" {
		env["FLEET_OWNER"] = a.owner
	}
	if a.goalID != "" {
		env["FLEET_GOAL_ID"] = a.goalID
	}
	return env
}

// tag sets the @fleet_* session options and the pane key environment, so the
// observer attributes the session whatever it reads first. Tag failures are
// not fatal: the session exists and answers, and only its attribution
// degrades. (The pane option -p is left to the window slice: the session
// environment covers the first pane, which is all F1 creates.)
func (e *Ensurer) tag(ctx context.Context, name, paneID, sessionID, owner, goalID string) error {
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
	if sessionID != "" {
		set("set-option", "-t", name, "@fleet_session_id", sessionID)
	}
	if owner != "" {
		set("set-option", "-t", name, "@fleet_owner", owner)
	}
	set("set-environment", "-t", name, "FLEET_PANE_KEY", key)
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// startID derives the PaneKey server start id from the server's pid and
// start time, the same rule as the control-mode handshake (commands.go), so
// a key from F1 matches the observer's on the same server.
func (e *Ensurer) startID(ctx context.Context) (string, error) {
	out, err := e.tmuxCall(ctx, "display-message", "-p", "#{pid} #{start_time}")
	if err != nil {
		return "", err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 2 {
		return "", fmt.Errorf("tmux display-message: unexpected output %q", out)
	}
	pid, err1 := strconv.ParseUint(f[0], 10, 32)
	start, err2 := strconv.ParseUint(f[1], 10, 63)
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("tmux display-message: unexpected output %q", out)
	}
	return startID(pid, start), nil
}

// startID joins the pid and start time in base 36 ([a-z0-9-] and short,
// ids.proto).
func startID(pid, start uint64) string {
	return strconv.FormatUint(pid, 36) + "-" + strconv.FormatUint(start, 36)
}
