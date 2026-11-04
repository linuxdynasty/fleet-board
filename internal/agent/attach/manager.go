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
// the stream and the PTY take care of the rest. target/cols/rows pin the
// command the id opened with, so a re-open that changes them is rejected
// instead of silently joining the wrong session. client and stream are
// written once by Open under the manager mutex (a concurrent Close may be
// reading them); every other field is immutable after creation. sendMu
// serializes the stream's Sends: the out pump and a racing Close send on
// different goroutines, and the stream races a slow browser.
type attach struct {
	id       string
	view     string
	socket   string
	server   string
	target   string
	cols     int
	rows     int
	readOnly bool
	ctx      context.Context
	cancel   context.CancelFunc
	client   *Client
	stream   Stream
	strip    Stripper
	sendMu   sync.Mutex
}

// send is the only way up-messages leave an attach: holding sendMu keeps
// the out pump's output and a racing Close's closed report from sharing
// one Send.
func (st *attach) send(req *agentv1.TerminalRequest) error {
	st.sendMu.Lock()
	defer st.sendMu.Unlock()
	return st.stream.Send(req)
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
		// tmux ids ($3) recycle on every server restart, so they never
		// name a session safely across runs: the caller must resolve one
		// to a name or session id first. Rejected, not retryable.
		return "", "", "", "", fmt.Errorf("attach: tmux-id targets are not served: %w", ErrInvalid)
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
	cols, rows := int(cmd.GetCols()), int(cmd.GetRows())
	m.mu.Lock()
	if st, ok := m.live[id]; ok {
		if st.readOnly != cmd.GetReadOnly() || st.target != target || st.cols != cols || st.rows != rows {
			m.mu.Unlock()
			return "", fmt.Errorf("attach: reopen %q changes the command: %w", id, ErrInvalid)
		}
		view := st.view
		m.mu.Unlock()
		return view, nil
	}
	stCtx, cancel := context.WithCancel(m.parent)
	st := &attach{
		id: id, view: view, socket: socket, server: server, target: target,
		cols: cols, rows: rows, readOnly: cmd.GetReadOnly(), ctx: stCtx, cancel: cancel,
	}
	m.live[id] = st
	m.mu.Unlock()
	// From here every failure cleans up only what this open made. teardown
	// forgets the id only when it still names this open (a retry may have
	// reused it), then kills this open's own client, view and context.
	// KillView, client Close and cancel are idempotent, so running beside
	// a concurrent finish of the same st is harmless.
	teardown := func(err error) (string, error) {
		m.mu.Lock()
		if cur, ok := m.live[id]; ok && cur == st {
			delete(m.live, id)
		}
		m.mu.Unlock()
		m.killSt(st, st.client)
		return "", err
	}
	// stale reports a lost race with a close: this open no longer owns
	// the id (a closer forgot it, or a retry reused it), so the loser
	// tears down only its own machinery and never calls finish(id),
	// which would either leak (id already gone) or kill the winner.
	stale := func(step string) error {
		m.mu.Lock()
		cur, ok := m.live[id]
		m.mu.Unlock()
		if !ok || cur != st {
			m.killSt(st, st.client)
			return fmt.Errorf("attach: %s raced a close: %w", step, ErrServerDown)
		}
		return nil
	}
	if err := m.views.CreateView(ctx, socket, target, view, cols, rows); err != nil {
		if errors.Is(err, ErrNoSession) {
			return teardown(fmt.Errorf("attach: open %q: %w", id, ErrNoSession))
		}
		return teardown(fmt.Errorf("attach: open %q: view: %w: %w", id, err, ErrServerDown))
	}
	client, err := OpenClient(m.opts.Program, socket, view, cols, rows, st.readOnly)
	if err != nil {
		return teardown(fmt.Errorf("attach: open %q: client: %w: %w", id, err, ErrServerDown))
	}
	m.mu.Lock()
	st.client = client
	m.mu.Unlock()
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
	m.mu.Lock()
	st.stream = stream
	m.mu.Unlock()
	if err := stale("dial"); err != nil {
		return "", err
	}
	ready := &agentv1.TerminalRequest{Up: &agentv1.TermUp{
		AttachId: &typev1.AttachId{Value: id},
		Body: &agentv1.TermUp_Ready{Ready: &agentv1.TermReady{
			Cols: cmd.GetCols(), Rows: cmd.GetRows(), ViewSessionName: view,
		}},
	}}
	// st.send, not stream.Send: a Close may be sending Closed beside us.
	if err := st.send(ready); err != nil {
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

// killSt tears down one open's own machinery: its client, its view, its
// context. It never touches the live map or another attach. client rides
// along because the caller read it race-free (its own write, or the copy
// under the manager mutex). Every step is idempotent (client Close is
// twice-safe, a missing view is success, cancel repeats freely), so a
// loser running beside a finish of the same st only repeats done work.
func (m *Manager) killSt(st *attach, client *Client) {
	if client != nil {
		client.Close()
	}
	if st.view != "" {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(m.parent), 5*time.Second)
		defer cancel()
		if err := m.views.KillView(ctx, st.socket, st.view); err != nil {
			m.log.WarnContext(ctx, "attach.kill_view", "attach_id", st.id, "view", st.view, "err", err)
		}
	}
	st.cancel()
}

// finish forgets exactly one attach and kills its machinery, once: the map
// delete under the mutex elects the winner, every loser returns without
// touching anything. sendClosed reports the reason where the stream still
// takes it; the send races the cancel and never blocks teardown. client
// and stream are copied under the mutex: an Open that lost the entry may
// still be writing them.
func (m *Manager) finish(id string, reason typev1.AttachCloseReason, sendClosed bool) {
	m.mu.Lock()
	st, ok := m.live[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	client, stream := st.client, st.stream
	delete(m.live, id)
	m.mu.Unlock()
	if sendClosed && stream != nil {
		go func() {
			_ = st.send(&agentv1.TerminalRequest{Up: &agentv1.TermUp{
				AttachId: &typev1.AttachId{Value: id},
				Body:     &agentv1.TermUp_Closed{Closed: &agentv1.TermClosed{Reason: reason}},
			}})
		}()
	}
	m.killSt(st, client)
	m.log.Info("attach.closed", "attach_id", id, "reason", reason.String())
}
