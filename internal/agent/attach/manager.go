package attach

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/creack/pty"
	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Manager errors: the handler maps them to the command result's reason.
var (
	// ErrInvalid is a malformed OpenAttach: id, size, target or server the
	// contract rules out. Not retryable (INVALID_ARGUMENT).
	ErrInvalid = errors.New("attach: invalid open")
	// ErrServerDown is a tmux, socket or stream failure. Retryable
	// (TMUX_SERVER_DOWN): the server may be restarting.
	ErrServerDown = errors.New("attach: tmux server is down")
)

// attachIDRE is the hub's attach id shape (lowercase ULID): it extends the
// view name, so it must fit viewNameRE's tail.
var attachIDRE = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// tmuxIDRE is a server-unique pane/session id like $3.
var tmuxIDRE = regexp.MustCompile(`^\$[0-9]{1,10}$`)

// outReadSize bounds one output read: well under the hub's 64 KiB chunk,
// so one read is one TermUp with no re-chunking.
const outReadSize = 32768

// dropAllWait bounds DropAll's wait for the pumps to exit after their
// cancel and PTY close; both unblock promptly, so this is paranoia.
const dropAllWait = 10 * time.Second

// landWait bounds one open's wait for its client to land on the view: a
// local attach lands in milliseconds, so ten seconds is a wedged client.
// On timeout the open fails and tears its view down for a clean retry.
const landWait = 10 * time.Second

// Stream is one agent Terminal stream: sends never return until the bytes
// are handed to HTTP/2 flow control, receives until the hub speaks or the
// stream dies. The production stream hangs off the session context, so it
// outlives the command that opened it and dies with the session.
type Stream interface {
	Send(*agentv1.TerminalRequest) error
	Receive() (*agentv1.TerminalResponse, error)
}

// Options builds a Manager. Program is the tmux binary; Runner and Caller
// run the one-shot tmux calls. Sockets maps a server kind to its socket
// path and short name, false when the agent does not serve it. ServerStart
// is the observer's current server-start id per name, for select keys.
// Dial opens the Terminal stream; Log, when nil, discards.
type Options struct {
	Program     string
	Runner      *proc.Runner
	Caller      proc.Caller
	ServerStart func(server string) (string, bool)
	Sockets     func(kind typev1.TmuxServerKind) (socket, server string, ok bool)
	Dial        func(ctx context.Context) (Stream, error)
	Log         *slog.Logger
}

// Manager owns the agent side of browser attaches: one grouped view, one
// PTY `tmux attach` client and one Terminal stream per attach id. Close
// leaves the target session running: only the view and its client die.
type Manager struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	opts   Options
	views  *Views
	parent context.Context
	live   map[string]*attach
	log    *slog.Logger
}

// attach is one open attach. ctx dies on teardown and unblocks both pumps;
// the stream and the PTY take care of the rest.
type attach struct {
	id       string
	view     string
	socket   string
	server   string
	readOnly bool
	ctx      context.Context
	cancel   context.CancelFunc
	client   *Client
	stream   Stream
	strip    Stripper
}

// New validates the options; parent is the agent run context, the pumps'
// ancestor. Sockets, ServerStart and Dial are required: without a server
// map, a start check or a stream there is no attach to open.
func New(parent context.Context, opts Options) (*Manager, error) {
	if opts.Program == "" || opts.Runner == nil || opts.Caller.Name() == "" {
		return nil, fmt.Errorf("attach: manager needs a tmux program, runner and caller: %w", ErrInvalid)
	}
	if opts.Sockets == nil || opts.ServerStart == nil || opts.Dial == nil {
		return nil, fmt.Errorf("attach: manager needs sockets, server starts and a dial: %w", ErrInvalid)
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Manager{
		opts:   opts,
		views:  &Views{Program: opts.Program, Runner: opts.Runner, Caller: opts.Caller, ServerStart: opts.ServerStart},
		parent: parent,
		live:   map[string]*attach{},
		log:    log,
	}, nil
}

// serverNames maps a SessionTarget server kind to the socket short name.
func serverName(kind typev1.TmuxServerKind) (string, bool) {
	switch kind {
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT:
		return "default", true
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET:
		return "fleet", true
	case typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE:
		return "fleet-probe", true
	default:
		return "", false
	}
}

// resolve maps an OpenAttach to its attach id, tmux target, socket and
// server name. Name and session-id arms name the session; the tmux-id arm
// passes a server-unique $id through (new-session takes it as a target).
func (m *Manager) resolve(cmd *agentv1.OpenAttach) (id, target, socket, server string, err error) {
	id = cmd.GetAttachId().GetValue()
	if !attachIDRE.MatchString(id) {
		return "", "", "", "", fmt.Errorf("attach: id %q must match [a-z0-9-]{1,32}: %w", id, ErrInvalid)
	}
	if cols, rows := cmd.GetCols(), cmd.GetRows(); !validDims(int(cols), int(rows)) {
		return "", "", "", "", fmt.Errorf("attach: cols %d and rows %d name a terminal: %w", cols, rows, ErrInvalid)
	}
	t := cmd.GetSession()
	if t == nil {
		return "", "", "", "", fmt.Errorf("attach: open names no session: %w", ErrInvalid)
	}
	name, ok := serverName(t.GetServer())
	if !ok {
		return "", "", "", "", fmt.Errorf("attach: unknown server kind: %w", ErrInvalid)
	}
	socket, server, ok = m.opts.Sockets(t.GetServer())
	if !ok || socket == "" || server == "" || server != name {
		return "", "", "", "", fmt.Errorf("attach: server %q is not served: %w", name, ErrServerDown)
	}
	switch by := t.GetBy().(type) {
	case *agentv1.SessionTarget_Name:
		target = by.Name
	case *agentv1.SessionTarget_SessionId:
		target = by.SessionId.GetValue()
	case *agentv1.SessionTarget_TmuxId:
		if !tmuxIDRE.MatchString(by.TmuxId) {
			return "", "", "", "", fmt.Errorf("attach: bad tmux id: %w", ErrInvalid)
		}
		target = by.TmuxId
	default:
		return "", "", "", "", fmt.Errorf("attach: open names no target: %w", ErrInvalid)
	}
	return id, target, socket, server, nil
}

// Open attaches id to its session: grouped view, PTY client, Terminal
// stream, armed self-destruct, then the pumps. Re-opening a live id
// returns its view: the command is declarative. A failed open tears its
// own view down, so a retry starts clean.
func (m *Manager) Open(ctx context.Context, cmd *agentv1.OpenAttach) (string, error) {
	id, target, socket, server, err := m.resolve(cmd)
	if err != nil {
		return "", err
	}
	view := "fleet-view-" + id
	m.mu.Lock()
	if st, ok := m.live[id]; ok {
		view := st.view
		m.mu.Unlock()
		return view, nil
	}
	stCtx, cancel := context.WithCancel(m.parent)
	st := &attach{id: id, view: view, socket: socket, server: server, readOnly: cmd.GetReadOnly(), ctx: stCtx, cancel: cancel}
	m.live[id] = st
	m.mu.Unlock()
	// From here every failure runs teardown, which forgets the id and
	// kills what this open made (only what it made: the view name derives
	// from the id, so it can never be another attach's).
	teardown := func(err error) (string, error) {
		m.finish(id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
		return "", err
	}
	stale := func(step string) error {
		m.mu.Lock()
		cur, ok := m.live[id]
		m.mu.Unlock()
		if !ok || cur != st {
			m.finish(id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
			return fmt.Errorf("attach: %s raced a close: %w", step, ErrServerDown)
		}
		return nil
	}
	if err := m.views.CreateView(ctx, socket, target, view, int(cmd.GetCols()), int(cmd.GetRows())); err != nil {
		if errors.Is(err, ErrNoSession) {
			return teardown(fmt.Errorf("attach: open %q: %w", id, ErrNoSession))
		}
		return teardown(fmt.Errorf("attach: open %q: view: %w: %w", id, err, ErrServerDown))
	}
	client, err := OpenClient(m.opts.Program, socket, view, int(cmd.GetCols()), int(cmd.GetRows()), st.readOnly)
	if err != nil {
		return teardown(fmt.Errorf("attach: open %q: client: %w: %w", id, err, ErrServerDown))
	}
	st.client = client
	// The client lands after config load and handshake, well after this
	// spawn returns: confirm it before pairing or arming, so a slow
	// attach never meets an armed-but-empty view (reaped) or a paired
	// hub (a blank bridge on a failed open).
	if err := m.views.WaitAttached(ctx, socket, view, landWait); err != nil {
		return teardown(fmt.Errorf("attach: open %q: land: %w", id, err))
	}
	if err := stale("land"); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return teardown(fmt.Errorf("attach: open %q: command ended: %w: %w", id, err, ErrServerDown))
	}
	stream, err := m.opts.Dial(ctx)
	if err != nil {
		return teardown(fmt.Errorf("attach: open %q: dial: %w: %w", id, err, ErrServerDown))
	}
	st.stream = stream
	if err := stale("dial"); err != nil {
		return "", err
	}
	ready := &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body: &agentv1.TermUp_Ready{Ready: &agentv1.TermReady{
			Cols: cmd.GetCols(), Rows: cmd.GetRows(), ViewSessionName: view,
		}},
	}}
	if err := stream.Send(ready); err != nil {
		return teardown(fmt.Errorf("attach: open %q: ready: %w: %w", id, err, ErrServerDown))
	}
	if err := stale("ready"); err != nil {
		return "", err
	}
	// The client is on the view now, so arm its self-destruct: the last
	// client leaving destroys the view even if this agent dies first.
	if err := m.views.ArmDestroy(ctx, socket, view); err != nil {
		return teardown(fmt.Errorf("attach: open %q: arm: %w: %w", id, err, ErrServerDown))
	}
	if err := stale("arm"); err != nil {
		return "", err
	}
	if m.parent.Err() != nil {
		// The run ended under this open (past a DropAll sweep): do not
		// serve an attach nobody will reap.
		return teardown(fmt.Errorf("attach: open %q: agent is stopping: %w", id, ErrServerDown))
	}
	m.wg.Add(2)
	go m.outPump(st)
	go m.inPump(st)
	m.log.InfoContext(st.ctx, "attach.opened", "attach_id", id, "view", view, "target", target, "read_only", st.readOnly)
	return view, nil
}

// Close ends an attach: the hub hears DETACHED, the PTY hangs up and the
// view dies (self-destruct, else KillView). Unknown ids are a no-op:
// closes are idempotent. The target session keeps running.
func (m *Manager) Close(id string) {
	m.finish(id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED, true)
}

// OpenIDs lists the live attach ids, sorted.
func (m *Manager) OpenIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// DropAll ends every attach for agent shutdown: the hub hears
// AGENT_SHUTDOWN where the stream still takes it, then every PTY and view
// dies. It waits for the pumps to exit, bounded.
func (m *Manager) DropAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.finish(id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_SHUTDOWN, true)
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(dropAllWait):
		m.log.Warn("attach.drop_slow", "open", len(ids))
	}
}

// finish forgets exactly one attach and kills its machinery, once: the map
// delete under the mutex elects the winner, every loser returns without
// touching anything. sendClosed reports the reason where the stream still
// takes it; the send races the cancel and never blocks teardown.
func (m *Manager) finish(id string, reason typev1.AttachCloseReason, sendClosed bool) {
	m.mu.Lock()
	st, ok := m.live[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.live, id)
	m.mu.Unlock()
	if sendClosed && st.stream != nil {
		go func() {
			_ = st.stream.Send(&agentv1.TerminalRequest{Up: &agentv1.TermUp{
				AttachId: &typev1.AttachId{Value: id},
				Body:     &agentv1.TermUp_Closed{Closed: &agentv1.TermClosed{Reason: reason}},
			}})
		}()
	}
	st.cancel()
	if st.client != nil {
		st.client.Close()
	}
	if st.view != "" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(m.parent), 5*time.Second)
		defer cancel()
		if err := m.views.KillView(ctx, st.socket, st.view); err != nil {
			m.log.WarnContext(ctx, "attach.kill_view", "attach_id", id, "view", st.view, "err", err)
		}
	}
	m.log.Info("attach.closed", "attach_id", id, "reason", reason.String())
}

// outPump renders the PTY to the hub: stripped, never dropped. A slow hub
// holds this pump in Send (HTTP/2 flow control), which holds the PTY, but
// never another attach. PTY death ends the attach as ERROR; a dead stream
// ends it silently, the hub already knows.
func (m *Manager) outPump(st *attach) {
	defer m.wg.Done()
	buf := make([]byte, outReadSize)
	id := &typev1.AttachId{Value: st.id}
	for {
		n, err := st.client.Master.Read(buf)
		if n > 0 {
			if clean := st.strip.Strip(buf[:n]); len(clean) > 0 {
				req := &agentv1.TerminalRequest{Up: &agentv1.TermUp{
					AttachId: id,
					Body:     &agentv1.TermUp_Output{Output: clean},
				}}
				if serr := st.stream.Send(req); serr != nil {
					m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
					return
				}
			}
		}
		if err != nil {
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, true)
			return
		}
	}
}

// inPump carries the hub into the PTY: input (dropped for viewers),
// resize, window and pane selects, and close. A select that fails (a stale
// key, a dying server) only skips that navigation: the attach lives on.
// Stream death ends the attach silently. The receive runs on its own
// goroutine so teardown (st.ctx) ends this pump even when the stream is
// wedged and never answers: the teardown forgets the id and kills the
// PTY and view, and the receiver exits with the stream.
func (m *Manager) inPump(st *attach) {
	defer m.wg.Done()
	type outcome struct {
		resp *agentv1.TerminalResponse
		err  error
	}
	downs := make(chan outcome)
	go func() {
		for {
			resp, err := st.stream.Receive()
			select {
			case downs <- outcome{resp: resp, err: err}:
			case <-st.ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		select {
		case <-st.ctx.Done():
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
			return
		case o := <-downs:
			if o.err != nil {
				m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_UNSPECIFIED, false)
				return
			}
			if m.handleDown(st, o.resp) {
				return
			}
		}
	}
}

// handleDown acts on one hub message; true ends the attach.
func (m *Manager) handleDown(st *attach, resp *agentv1.TerminalResponse) bool {
	down := resp.GetDown()
	if down == nil || down.GetAttachId().GetValue() != st.id {
		return false
	}
	switch body := down.GetBody().(type) {
	case *agentv1.TermDown_Input:
		if len(body.Input) == 0 || st.readOnly {
			return false
		}
		if err := writeFull(st.client.Master, body.Input); err != nil {
			m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR, true)
			return true
		}
	case *agentv1.TermDown_Resize:
		cols, rows := int(body.Resize.GetCols()), int(body.Resize.GetRows())
		if !validDims(cols, rows) {
			m.log.Debug("attach.resize_invalid", "attach_id", st.id, "cols", cols, "rows", rows)
			return false
		}
		// A dying client errors here; the out pump reports it.
		// Cells fit: validDims bounds both to 1..1000.
		rows16 := uint16(rows) //nolint:gosec // G115: as above
		cols16 := uint16(cols) //nolint:gosec // G115: as above
		_ = pty.Setsize(st.client.Master, &pty.Winsize{Rows: rows16, Cols: cols16})
	case *agentv1.TermDown_Select:
		m.selectOn(st, body.Select)
	case *agentv1.TermDown_Close:
		m.finish(st.id, typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED, true)
		return true
	}
	return false
}

// selectOn moves the view's current window or active pane. Only the
// attach's own server is selectable; a key from another server or a stale
// generation is refused before it can land anywhere.
func (m *Manager) selectOn(st *attach, sel *agentv1.TermSelect) {
	switch sel.GetTarget().(type) {
	case *agentv1.TermSelect_Window:
		w := sel.GetWindow()
		if w.GetServer() != st.server {
			m.log.Debug("attach.select_server", "attach_id", st.id, "server", w.GetServer())
			return
		}
		if err := m.views.SelectWindow(st.ctx, st.socket, w.GetServer(), w.GetServerStartId(), st.view, w.GetWindowId()); err != nil {
			m.log.Debug("attach.select_window", "attach_id", st.id, "err", err)
		}
	case *agentv1.TermSelect_Pane:
		p := sel.GetPane()
		if p.GetServer() != st.server {
			m.log.Debug("attach.select_server", "attach_id", st.id, "server", p.GetServer())
			return
		}
		if err := m.views.SelectPane(st.ctx, st.socket, p.GetServer(), p.GetServerStartId(), st.view, p.GetPaneId()); err != nil {
			m.log.Debug("attach.select_pane", "attach_id", st.id, "err", err)
		}
	default:
		m.log.Debug("attach.select_unknown", "attach_id", st.id)
	}
}

func writeFull(f interface{ Write([]byte) (int, error) }, data []byte) error {
	for len(data) > 0 {
		n, err := f.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return errors.New("attach: short write to the client")
		}
		data = data[n:]
	}
	return nil
}
