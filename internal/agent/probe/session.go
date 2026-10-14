package probe

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Backend is the tmux surface one probe pass needs on the fleet-probe
// server. Every method names a session; pane targets are always the
// session's first pane, the only pane a probe session ever has. *TMux is
// the production tmux-CLI implementation; tests use a fake. Errors are the
// tmux failure with its stderr attached: the pass reports fixed messages
// and never forwards backend text (screens are untrusted).
type Backend interface {
	// HasSession reports whether the named session exists.
	HasSession(ctx context.Context, name string) (bool, error)
	// NewSession starts the named session detached in dir running binary
	// (a bare name, no arguments: profiles land in a later slice).
	NewSession(ctx context.Context, name, dir, binary string) error
	// KillSession ends the named session. A missing session is nil, so
	// StopProbe stays declarative.
	KillSession(ctx context.Context, name string) error
	// Capture reads the first pane's visible screen with styling kept
	// (capture-pane -p -e): the Claude placeholder check needs the
	// escapes, and markers strip them back off.
	Capture(ctx context.Context, name string) (string, error)
	// SendCommand types text at the first pane as literal text plus one
	// Enter: the only keys a pass ever sends.
	SendCommand(ctx context.Context, name, text string) error
	// PaneDead reports whether the first pane is dead.
	PaneDead(ctx context.Context, name string) (bool, error)
	// PaneCommand is the first pane's running command name, for telling a
	// crashed harness (a shell) from a busy one.
	PaneCommand(ctx context.Context, name string) (string, error)
	// ServerStartID is the PaneKey server start id of the probe server.
	ServerStartID(ctx context.Context) (string, error)
	// FirstPane is the first pane's tmux id (such as "%0").
	FirstPane(ctx context.Context, name string) (string, error)
}

// Runner runs one tmux CLI call. *proc.Runner satisfies it; the seam keeps
// tests on the production path (a throwaway server) instead of a fake.
type Runner interface {
	Run(ctx context.Context, sp proc.Spec) (proc.Result, error)
}

// TMux is the Backend on one tmux server reached by CLI one-shots. It
// holds no control client: the pass needs a handful of calls per ten
// minutes, and one-shots keep the probe independent of the observer.
type TMux struct {
	runner Runner
	caller proc.Caller
	tmux   string
	socket string
}

// NewTMux checks the setup and fails fast.
func NewTMux(runner Runner, tmuxPath, socket string) (*TMux, error) {
	if runner == nil {
		return nil, errors.New("probe: Runner is required")
	}
	if socket == "" {
		return nil, errors.New("probe: socket is required (there is no default server)")
	}
	caller, err := proc.For("tmux")
	if err != nil {
		return nil, err
	}
	if tmuxPath == "" {
		tmuxPath = "tmux"
	}
	return &TMux{runner: runner, caller: caller, tmux: tmuxPath, socket: socket}, nil
}

// call runs one tmux CLI call on the bound server and returns its stdout.
// Arguments are separate elements, never a shell string.
func (t *TMux) call(ctx context.Context, args ...string) (string, error) {
	argv := append([]string{"-S", t.socket}, args...)
	res, err := t.runner.Run(ctx, proc.Spec{Caller: t.caller, Path: t.tmux, Args: argv})
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

// isDown matches the connection failures tmux prints across versions: "no
// server running on ...", "error connecting to ...", "failed to connect".
func isDown(msg string) bool {
	for _, s := range []string{"no server running", "error connecting", "failed to connect", "no such file or directory"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// HasSession reports whether the named session exists. has-session prints
// nothing on success.
func (t *TMux) HasSession(ctx context.Context, name string) (bool, error) {
	_, err := t.call(ctx, "has-session", "-t", name)
	if err != nil {
		if strings.Contains(err.Error(), "can't find session") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// NewSession starts the named session detached in dir running binary. The
// binary is one PATH-resolved program name with no arguments and no
// whitespace: tmux would join several words back into a shell string.
func (t *TMux) NewSession(ctx context.Context, name, dir, binary string) error {
	if dir == "" || binary == "" || strings.ContainsAny(binary, " \t\n") {
		return fmt.Errorf("tmux new-session: bad dir or binary")
	}
	_, err := t.call(ctx, "new-session", "-d", "-s", name, "-c", dir, binary)
	return err
}

// KillSession ends the named session. A missing session or a missing
// server reads as already gone, so StopProbe stays declarative.
func (t *TMux) KillSession(ctx context.Context, name string) error {
	_, err := t.call(ctx, "kill-session", "-t", name)
	if err != nil && (strings.Contains(err.Error(), "can't find session") || isDown(err.Error())) {
		return nil
	}
	return err
}

// paneTarget is the session's first pane. Probe sessions hold one pane;
// anything else on the probe server is not ours to touch.
func paneTarget(name string) string { return name + ":0.0" }

// Capture reads the first pane's visible screen with escapes kept.
func (t *TMux) Capture(ctx context.Context, name string) (string, error) {
	return t.call(ctx, "capture-pane", "-p", "-e", "-t", paneTarget(name))
}

// maxCommandBytes caps typed text: status commands are a few bytes, and
// anything longer is not a command the probe knows.
const maxCommandBytes = 256

// SendCommand types text literally plus one Enter. -l stops tmux reading
// key names inside the text and "--" stops a leading "-" reading as a
// flag; Enter comes from the closed key set, never from the text.
func (t *TMux) SendCommand(ctx context.Context, name, text string) error {
	if len(text) == 0 || len(text) > maxCommandBytes || strings.ContainsAny(text, "\n\x00") {
		return fmt.Errorf("tmux send-keys: refused text")
	}
	for i := 0; i < len(text); i++ {
		if text[i] < 0x20 || text[i] == 0x7f {
			return fmt.Errorf("tmux send-keys: refused text")
		}
	}
	target := paneTarget(name)
	if _, err := t.call(ctx, "send-keys", "-t", target, "-l", "--", text); err != nil {
		return err
	}
	_, err := t.call(ctx, "send-keys", "-t", target, "Enter")
	return err
}

// PaneDead reports whether the first pane is dead.
func (t *TMux) PaneDead(ctx context.Context, name string) (bool, error) {
	out, err := t.call(ctx, "display-message", "-p", "-t", paneTarget(name), "#{pane_dead}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// PaneCommand is the first pane's running command name.
func (t *TMux) PaneCommand(ctx context.Context, name string) (string, error) {
	out, err := t.call(ctx, "display-message", "-p", "-t", paneTarget(name), "#{pane_current_command}")
	if err != nil {
		return "", err
	}
	cmd, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	return cmd, nil
}

// ServerStartID mints the PaneKey server start id from the server's pid
// and start time, the same rule as the control-mode handshake, so a key
// from a probe matches the observer's on the same server.
func (t *TMux) ServerStartID(ctx context.Context) (string, error) {
	out, err := t.call(ctx, "display-message", "-p", "#{pid} #{start_time}")
	if err != nil {
		return "", err
	}
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) != 2 {
		return "", fmt.Errorf("tmux display-message: unexpected output")
	}
	pid, err1 := strconv.ParseUint(f[0], 10, 32)
	start, err2 := strconv.ParseUint(f[1], 10, 63)
	if err1 != nil || err2 != nil {
		return "", fmt.Errorf("tmux display-message: unexpected output")
	}
	// [a-z0-9-] and short: the pid and start time in base 36 (ids.proto).
	return strconv.FormatUint(pid, 36) + "-" + strconv.FormatUint(start, 36), nil
}

// FirstPane is the first pane's tmux id.
func (t *TMux) FirstPane(ctx context.Context, name string) (string, error) {
	out, err := t.call(ctx, "list-panes", "-t", name, "-F", "#{pane_id}")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if id == "" || id[0] != '%' {
		return "", fmt.Errorf("tmux list-panes: unexpected output")
	}
	return id, nil
}
