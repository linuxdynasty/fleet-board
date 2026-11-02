package attach

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// ErrNoSession is a view target that is not on the named server. The
// manager maps it to SESSION_NOT_FOUND.
var ErrNoSession = errors.New("attach: session is not on this server")

var (
	// nameRE is the hub session shape, the only target an attach names.
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// viewNameRE is the view an attach makes: the prefix plus its ULID.
	viewNameRE = regexp.MustCompile(`^fleet-view-[a-z0-9-]{1,32}$`)
	// paneIDRE is a tmux pane id like %3.
	paneIDRE = regexp.MustCompile(`^%[0-9]{1,10}$`)
	// windowIDRE is a tmux window id like @3.
	windowIDRE = regexp.MustCompile(`^@[0-9]{1,10}$`)
)

// Views creates and drives grouped view sessions on tmux servers: one per
// attach, on the socket the attach names. A view groups with the target
// session (the same windows) and is only ever addressed by its own name:
// selects run in the view session, never the target. The manager arms
// destroy-unattached on the view after its client attaches, so the last
// client leaving destroys it.
type Views struct {
	// Program is the tmux binary.
	Program string
	// Runner runs the one-shot tmux calls; Caller is proc.For("tmux").
	Runner *proc.Runner
	Caller proc.Caller
	// ServerStart is the observer's current server-start id per socket
	// name: a select key from an older server generation is refused
	// before it can land on a reused pane or window id.
	ServerStart func(server string) (string, bool)
}

// CreateView groups a view session with the target at the wanted size.
// A missing target fails instead of making an ungrouped view. The view is
// born without destroy-unattached: that option reaps a session with no
// client attached yet, so the manager arms it (ArmDestroy) only after the
// attach's client is on the view.
func (v *Views) CreateView(ctx context.Context, socket, target, view string, cols, rows int) error {
	if !nameRE.MatchString(target) {
		return fmt.Errorf("attach: target %q must match [a-z0-9][a-z0-9-]{0,31}", target)
	}
	if !viewNameRE.MatchString(view) {
		return fmt.Errorf("attach: view %q must match fleet-view-[a-z0-9-]{1,32}", view)
	}
	if !validDims(cols, rows) {
		return fmt.Errorf("attach: cols %d and rows %d name a terminal", cols, rows)
	}
	// new-session -t on a missing name silently starts a new group, so
	// check first: a missing target fails instead of making an ungrouped
	// view of a fresh shell.
	if out, err := v.tmux(ctx, socket, "has-session", "-t", target); err != nil {
		if strings.Contains(err.Error()+out, "can't find session") {
			return fmt.Errorf("attach: target %q is not on this server: %w", target, ErrNoSession)
		}
		return fmt.Errorf("attach: has-session %q: %w: %s", target, err, out)
	}
	if out, err := v.tmux(ctx, socket, "new-session", "-d", "-t", target, "-s", view,
		"-x", strconv.Itoa(cols), "-y", strconv.Itoa(rows)); err != nil {
		return fmt.Errorf("attach: group view %q with %q: %w: %s", view, target, err, out)
	}
	// The target can die between has-session and new-session, which would
	// leave the same ungrouped view; verify the grouping took and clean
	// up the view when it did not.
	viewGroup, verr := v.tmux(ctx, socket, "display-message", "-p", "-t", view, "#{session_group}")
	targetGroup, terr := v.tmux(ctx, socket, "display-message", "-p", "-t", target, "#{session_group}")
	if verr != nil || terr != nil || viewGroup == "" || viewGroup != targetGroup {
		_ = v.KillView(ctx, socket, view)
		return fmt.Errorf("attach: view %q did not group with %q", view, target)
	}
	return nil
}

// ArmDestroy sets destroy-unattached on a view whose client is already
// attached, so the view destroys itself when the last client leaves. Call
// it only after the attach's client opened, never on a view with no client
// yet: tmux reaps a never-attached session with this option set.
func (v *Views) ArmDestroy(ctx context.Context, socket, view string) error {
	if !viewNameRE.MatchString(view) {
		return fmt.Errorf("attach: view %q must match fleet-view-[a-z0-9-]{1,32}", view)
	}
	if out, err := v.tmux(ctx, socket, "set-option", "-t", view, "destroy-unattached", "on"); err != nil {
		return fmt.Errorf("attach: destroy-unattached on %q: %w: %s", view, err, out)
	}
	return nil
}

// landPoll is how often WaitAttached asks for the view's clients. The
// caller bounds the whole wait: a local attach lands in milliseconds, so
// seconds are a wedged client, not a slow one.
const landPoll = 50 * time.Millisecond

// WaitAttached waits until a client is on the view: the attach the
// manager just spawned landed. It returns then, so the manager arms the
// view's self-destruct only with a client holding it: arming an empty
// view lets tmux reap it before a slow attach lands. A vanished view or
// the timeout fails: the caller tears the open down.
func (v *Views) WaitAttached(ctx context.Context, socket, view string, timeout time.Duration) error {
	if !viewNameRE.MatchString(view) {
		return fmt.Errorf("attach: view %q must match fleet-view-[a-z0-9-]{1,32}", view)
	}
	deadline := time.Now().Add(timeout)
	for {
		out, err := v.tmux(ctx, socket, "list-clients", "-t", view, "-F", "#{client_name}")
		switch {
		case err == nil && strings.TrimSpace(out) != "":
			return nil
		case err != nil && strings.Contains(err.Error()+out, "can't find session"):
			// The unarmed view is never reaped, so a vanished view is
			// a dead server: fail fast, not at the deadline, and let
			// the caller tear down for a clean retry.
			return fmt.Errorf("attach: view %q is gone: %w", view, ErrServerDown)
		case err != nil:
			return fmt.Errorf("attach: list view clients: %w: %s", err, out)
		case time.Now().After(deadline):
			// The server side is wedged and a retry opens clean (the
			// caller kills this view): never the request.
			return fmt.Errorf("attach: client never landed on %q: %w", view, ErrServerDown)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("attach: wait for the client: %w", ctx.Err())
		case <-time.After(landPoll):
		}
	}
}

// KillView kills a view session, best-effort. A view that already
// self-destroyed (or was never made) reports "can't find session", which is
// success here; any other error is returned.
func (v *Views) KillView(ctx context.Context, socket, view string) error {
	if !viewNameRE.MatchString(view) {
		return fmt.Errorf("attach: view %q must match fleet-view-[a-z0-9-]{1,32}", view)
	}
	if out, err := v.tmux(ctx, socket, "kill-session", "-t", view); err != nil {
		if strings.Contains(err.Error()+out, "can't find session") {
			return nil
		}
		return fmt.Errorf("attach: kill view %q: %w: %s", view, err, out)
	}
	return nil
}

// SelectWindow moves the view session's current window to the named id.
// The key must name this server's generation and a window of the attached
// session; the target's own current window never moves.
func (v *Views) SelectWindow(ctx context.Context, socket, server, startID, view, windowID string) error {
	if !viewNameRE.MatchString(view) || !windowIDRE.MatchString(windowID) {
		return fmt.Errorf("attach: bad select-window key")
	}
	if err := v.checkServer(server, startID); err != nil {
		return err
	}
	out, err := v.tmux(ctx, socket, "list-windows", "-t", view, "-F", "#{window_id} #{window_index}")
	if err != nil {
		return fmt.Errorf("attach: list view windows: %w: %s", err, out)
	}
	index := ""
	for line := range strings.Lines(strings.TrimSpace(out)) {
		if id, idx, ok := strings.Cut(strings.TrimSpace(line), " "); ok && id == windowID {
			index = idx
		}
	}
	if index == "" {
		return fmt.Errorf("attach: window %q is not in the attached session", windowID)
	}
	if out, err := v.tmux(ctx, socket, "select-window", "-t", view+":"+index); err != nil {
		return fmt.Errorf("attach: select view window: %w: %s", err, out)
	}
	return nil
}

// SelectPane moves the view session's active pane to the named id. The
// key must name this server's generation and a pane of the attached
// session, so a stale key can never land on a reused pane id.
func (v *Views) SelectPane(ctx context.Context, socket, server, startID, view, paneID string) error {
	if !viewNameRE.MatchString(view) || !paneIDRE.MatchString(paneID) {
		return fmt.Errorf("attach: bad select-pane key")
	}
	if err := v.checkServer(server, startID); err != nil {
		return err
	}
	out, err := v.tmux(ctx, socket, "list-panes", "-s", "-t", view, "-F", "#{pane_id}")
	if err != nil {
		return fmt.Errorf("attach: list view panes: %w: %s", err, out)
	}
	found := false
	for line := range strings.Lines(strings.TrimSpace(out)) {
		if strings.TrimSpace(line) == paneID {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("attach: pane %q is not in the attached session", paneID)
	}
	if out, err := v.tmux(ctx, socket, "select-pane", "-t", paneID); err != nil {
		return fmt.Errorf("attach: select view pane: %w: %s", err, out)
	}
	return nil
}

// checkServer refuses a select key from another server or an older
// server generation.
func (v *Views) checkServer(server, startID string) error {
	if server == "" || startID == "" {
		return fmt.Errorf("attach: a select key names its server and start")
	}
	current, ok := v.ServerStart(server)
	if !ok {
		return fmt.Errorf("attach: server %q is not observed", server)
	}
	if current != startID {
		return fmt.Errorf("attach: key is from an older server generation")
	}
	return nil
}

// validDims bounds window sizes: 1 to 1000 is a terminal, 0 or more is
// not.
func validDims(cols, rows int) bool {
	return cols >= 1 && cols <= 1000 && rows >= 1 && rows <= 1000
}

func (v *Views) tmux(ctx context.Context, socket string, args ...string) (string, error) {
	full := append([]string{"-S", socket}, args...)
	res, err := v.Runner.Run(ctx, proc.Spec{Caller: v.Caller, Path: v.Program, Args: full})
	if err != nil {
		return strings.TrimSpace(string(res.Stdout) + string(res.Stderr)), err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}
