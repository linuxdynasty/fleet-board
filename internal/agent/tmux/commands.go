package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// Errors the typed commands return.
var (
	// ErrNotAttached: the client is not up (connecting, absent, stopped).
	ErrNotAttached = errors.New("tmux: control client not attached")
	// ErrWrongServer: a typing.Line built for another server, or for an
	// earlier start of this one (its pane id may have been reused).
	ErrWrongServer = errors.New("tmux: line is for another server or server start")
	// ErrCommandFailed: tmux answered %error. The reply text is tmux's and
	// can echo pane or session text: it is not in the error.
	ErrCommandFailed = errors.New("tmux: command failed")
	// ErrBadReply: a reply that is not in the shape the command asked for,
	// or one the reply bound cut short.
	ErrBadReply = errors.New("tmux: reply not in the asked shape")
	// ErrBadSubscription: a subscription name, target or format that could
	// break the refresh-client -B command or the %subscription-changed line.
	ErrBadSubscription = errors.New("tmux: subscription refused")
)

// current returns the attached conn, or ErrNotAttached.
func (c *Client) current() (*conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.state != StateUp {
		return nil, ErrNotAttached
	}
	return c.conn, nil
}

// currentFor returns the attached conn when it is the server and server
// start the line was built for. The check and the pick are one hold of mu,
// and attach sets conn and startID in one hold too, so a line for an older
// start can never be written on a newer server's conn.
func (c *Client) currentFor(server, start string) (*conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if server != c.opts.Server || start != c.startID {
		return nil, ErrWrongServer
	}
	if c.conn == nil || c.state != StateUp {
		return nil, ErrNotAttached
	}
	return c.conn, nil
}

// run sends one command on the attached client and returns its reply; a
// %error reply is ErrCommandFailed.
func (c *Client) run(ctx context.Context, line string, continuePane PaneID) (Reply, error) {
	cn, err := c.current()
	if err != nil {
		return Reply{}, err
	}
	return c.on(ctx, cn, line, continuePane)
}

// on sends one command on cn and returns its reply; a %error reply is
// ErrCommandFailed, a conn that ended is ErrNotAttached.
func (c *Client) on(ctx context.Context, cn *conn, line string, continuePane PaneID) (Reply, error) {
	r, err := cn.do(ctx, line, continuePane)
	if errors.Is(err, errConnClosed) {
		return Reply{}, ErrNotAttached
	}
	if err != nil {
		return Reply{}, err
	}
	if r.Failed {
		return r, ErrCommandFailed
	}
	return r, nil
}

// handshakeFormat is read once per attach: the control client's own flags
// (the version gate reads client_flags, never trusts -F: spike 0.5 finding
// 8), the server's pid and start time (the start id), and its version.
const handshakeFormat = "#{client_flags}|#{pid}|#{start_time}|#{version}"

type serverInfo struct {
	startID string
	version string
}

var serverVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// handshake checks that tmux took both flags and reads the server's start.
func (c *Client) handshake(ctx context.Context, cn *conn) (serverInfo, error) {
	r, err := cn.do(ctx, "display-message -p '"+handshakeFormat+"'", NoID)
	if err != nil {
		return serverInfo{}, fmt.Errorf("tmux: handshake: %w", err)
	}
	if r.Failed || len(r.Body) != 1 {
		return serverInfo{}, fmt.Errorf("tmux: handshake: %w", ErrBadReply)
	}
	f := strings.Split(r.Body[0], "|")
	if len(f) != 4 {
		return serverInfo{}, fmt.Errorf("tmux: handshake: %w", ErrBadReply)
	}
	if !hasFlags(f[0]) {
		return serverInfo{}, ErrTooOld
	}
	pid, err1 := strconv.ParseUint(f[1], 10, 32)
	start, err2 := strconv.ParseUint(f[2], 10, 63)
	if err1 != nil || err2 != nil || !serverVersionRE.MatchString(f[3]) {
		return serverInfo{}, fmt.Errorf("tmux: handshake: %w", ErrBadReply)
	}
	// [a-z0-9-] and short (ids.proto): the pid and start time in base 36.
	id := strconv.FormatUint(pid, 36) + "-" + strconv.FormatUint(start, 36)
	return serverInfo{startID: id, version: f[3]}, nil
}

// hasFlags reports whether client_flags lists ignore-size and the
// pause-after the client asked for.
func hasFlags(flags string) bool {
	var size, pause bool
	for f := range strings.SplitSeq(flags, ",") {
		size = size || f == "ignore-size"
		pause = pause || f == "pause-after="+strconv.Itoa(int(PauseAfter.Seconds()))
	}
	return size && pause
}

// Send sends one typing.Line. It refuses the zero Line (typing.ErrZeroLine)
// and a Line whose server or server start id is not the attached one
// (ErrWrongServer), before anything is written. The caller that built the
// line calls typing.Recorder.Sent when Send returns nil.
func (c *Client) Send(ctx context.Context, l typing.Line) error {
	cmd, err := l.Command()
	if err != nil {
		return err
	}
	cn, err := c.currentFor(l.Server(), l.ServerStartID())
	if err != nil {
		return err
	}
	_, err = c.on(ctx, cn, cmd, NoID)
	return err
}

// Capture is a pane's visible screen as capture-pane prints it. Lines are
// pane text: untrusted, never logged; ANSI stripping and the byte cap are
// the caller's (02e).
type Capture struct {
	Lines []string
	// Truncated is set when the reply bound dropped lines.
	Truncated bool
}

// Capture reads pane's visible screen (capture-pane -p).
func (c *Client) Capture(ctx context.Context, pane PaneID) (Capture, error) {
	if pane < 0 {
		return Capture{}, fmt.Errorf("tmux: capture: %w", ErrBadReply)
	}
	r, err := c.run(ctx, "capture-pane -p -t "+pane.String(), NoID)
	if err != nil {
		return Capture{}, err
	}
	return Capture{Lines: r.Body, Truncated: r.Dropped > 0}, nil
}

// NoStatus is Pane.DeadStatus when tmux has no exit status (a live pane).
const NoStatus = -1

// Pane is one pane as list-panes -a reports it: ids and numbers only, never
// a name (names are the developer's text; 7d reads them).
type Pane struct {
	Session SessionID
	Window  WindowID
	ID      PaneID
	// Dead is set for a pane whose program exited and that tmux kept
	// (remain-on-exit); DeadStatus is then its exit status, else NoStatus.
	Dead          bool
	DeadStatus    int
	Width, Height int
}

const panesFormat = "#{session_id} #{window_id} #{pane_id} #{pane_dead} #{pane_dead_status} #{pane_width} #{pane_height}"

// ListPanes lists every pane on the server (list-panes -a).
func (c *Client) ListPanes(ctx context.Context) ([]Pane, error) {
	r, err := c.run(ctx, "list-panes -a -F '"+panesFormat+"'", NoID)
	if err != nil {
		return nil, err
	}
	if r.Dropped > 0 {
		return nil, fmt.Errorf("tmux: list-panes: %w", ErrBadReply)
	}
	panes := make([]Pane, 0, len(r.Body))
	for _, line := range r.Body {
		p, ok := parsePane(line)
		if !ok {
			return nil, fmt.Errorf("tmux: list-panes: %w", ErrBadReply)
		}
		panes = append(panes, p)
	}
	return panes, nil
}

func parsePane(line string) (Pane, bool) {
	f := strings.Split(line, " ")
	if len(f) != 7 {
		return Pane{}, false
	}
	s, ok1 := parseID([]byte(f[0]), '$')
	w, ok2 := parseID([]byte(f[1]), '@')
	p, ok3 := parseID([]byte(f[2]), '%')
	wd, err1 := strconv.Atoi(f[5])
	ht, err2 := strconv.Atoi(f[6])
	if !ok1 || !ok2 || !ok3 || err1 != nil || err2 != nil || (f[3] != "0" && f[3] != "1") {
		return Pane{}, false
	}
	pane := Pane{Session: SessionID(s), Window: WindowID(w), ID: PaneID(p), Dead: f[3] == "1", DeadStatus: NoStatus, Width: wd, Height: ht}
	if pane.Dead && f[4] != "" {
		st, err := strconv.Atoi(f[4])
		if err != nil {
			return Pane{}, false
		}
		pane.DeadStatus = st
	}
	return pane, true
}

// Continue asks tmux to resume a paused pane (refresh-client -A
// '%N:continue'). The pane counts as resumed only when this command's own
// reply holds "%continue %N" (01 E2).
func (c *Client) Continue(ctx context.Context, pane PaneID) error {
	if pane < 0 {
		return fmt.Errorf("tmux: continue: %w", ErrBadReply)
	}
	_, err := c.run(ctx, "refresh-client -A '"+pane.String()+":continue'", pane)
	return err
}

// Paused reports whether tmux paused pane's output to this client and no
// continue of the client's own has resumed it since.
func (c *Client) Paused(pane PaneID) bool {
	cn, err := c.current()
	if err != nil {
		return false
	}
	return cn.isPaused(pane)
}
