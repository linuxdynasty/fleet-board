package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// PauseAfter is the pause-after flag the control client attaches with:
// tmux pauses a pane whose output has waited this long for the client
// (defaults, "tmux control client").
const PauseAfter = 1 * time.Second

// attachFlags is what the client attaches with: ignore-size so it never
// changes the size of the developer's windows (set at attach: turned on
// later it does not undo a shrink, spike 0.5 finding 9), and pause-after.
var attachFlags = fmt.Sprintf("ignore-size,pause-after=%d", int(PauseAfter/time.Second))

// Backoff between attach attempts while the server is there (defaults,
// "tmux control client"): after n failures in a row, uniform from base*2^(n-1)
// to base*2^n, capped at backoffMax. An attach that reaches StateUp resets n.
const (
	backoffBase = time.Second
	backoffMax  = 60 * time.Second
)

// ClientOptions configure a [Client]. There is no default server: the
// caller names the socket (the agent builds the developer's default-server
// target in one place, in cmd).
type ClientOptions struct {
	// Program is the tmux binary: the one the agent's machine facts found.
	Program string
	// Socket is the server's socket path, passed as -S.
	Socket string
	// Server is the PaneKey.server name: "default", "fleet" or
	// "fleet-probe". Send refuses a line built for another.
	Server string
	// Runner runs the one-shot `tmux -V` and `tmux list-sessions` under the
	// proc caller "tmux".
	Runner *proc.Runner
	// Telemetry is where the gauge and the log events go; nil counts and
	// logs nowhere.
	Telemetry *telemetry.Telemetry
	// Notify gets every message that is not a reply (output, %pause,
	// %exit, subscription changes, window and session changes) on the
	// reader goroutine, in order. It must return quickly and must not call
	// the Client: the reader waits for it, and so do replies.
	Notify func(Message)
	// Parser bounds each line and reply (zero: the defaults).
	Parser Options
}

// State is where a client is.
type State int

// The states.
const (
	StateConnecting State = iota // attaching, or waiting to retry
	StateUp                      // attached, flags checked, subscriptions sent
	StateAbsent                  // no server on the socket: waits for Wake, no timer
	StateTooOld                  // tmux older than 3.2 or without the flags: Run has returned
	StateStopped                 // Run has returned for its context
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateUp:
		return "up"
	case StateAbsent:
		return "absent"
	case StateTooOld:
		return "too_old"
	case StateStopped:
		return "stopped"
	}
	return "unknown"
}

// Client is one tmux control-mode client for one server (01 E2). Run keeps
// it attached; the typed methods send one command each and wait for its
// reply. Safe for concurrent use.
type Client struct {
	opts   ClientOptions
	kind   string // the server_kind label
	caller proc.Caller
	tel    *clientTelemetry
	wake   chan struct{}
	tee    io.Writer // tests only (SetTeeForTest): a copy of the raw stream

	attempts atomic.Int64 // attach tries started (AttemptsForTest)

	mu      sync.Mutex
	state   State
	changed chan struct{} // closed and replaced on every state change
	conn    *conn         // the attached client, nil when not StateUp
	session SessionID
	startID string
	subs    []Subscription
}

// NewClient checks opts and returns a client that is not yet attached.
func NewClient(opts ClientOptions) (*Client, error) {
	kind, ok := serverKinds[opts.Server]
	switch {
	case opts.Program == "":
		return nil, errors.New("tmux: ClientOptions.Program is required")
	case opts.Socket == "":
		return nil, errors.New("tmux: ClientOptions.Socket is required (there is no default server)")
	case !ok:
		return nil, fmt.Errorf("tmux: ClientOptions.Server %q is not default, fleet or fleet-probe", opts.Server)
	case opts.Runner == nil:
		return nil, errors.New("tmux: ClientOptions.Runner is required")
	}
	caller, err := proc.For("tmux")
	if err != nil {
		return nil, err
	}
	tel, err := newClientTelemetry(opts.Telemetry, kind)
	if err != nil {
		return nil, err
	}
	return &Client{
		opts: opts, kind: kind, caller: caller, tel: tel,
		wake:    make(chan struct{}, 1),
		session: NoID,
		changed: make(chan struct{}),
	}, nil
}

// serverKinds maps PaneKey.server to the server_kind label (the same words
// typing.sent logs).
var serverKinds = map[string]string{"default": "default", "fleet": "fleet", "fleet-probe": "probe"}

// Run attaches and keeps the client attached until ctx ends (it returns
// ctx's error) or tmux is too old (ErrTooOld). After an attach ends, it
// retries with backoff while the server is there; when no server answers
// it waits in StateAbsent for Wake, with no timer (row-7 ruling fork 4).
// Run is called once.
func (c *Client) Run(ctx context.Context) error {
	failures, absent := 0, false
	for {
		reason, err := c.attach(ctx)
		switch {
		case ctx.Err() != nil:
			if err == nil { // it was up: a shutdown, not a failure
				c.tel.exit(context.WithoutCancel(ctx), reasonCancelled)
			}
			c.setState(StateStopped)
			return ctx.Err()
		case errors.Is(err, ErrTooOld):
			c.tel.exit(ctx, reasonTooOld)
			c.setState(StateTooOld)
			return ErrTooOld
		case errors.Is(err, errAbsent):
			if !absent { // logged once per absence, not per wake
				c.tel.exit(ctx, reasonAbsent)
			}
			c.setState(StateAbsent)
			failures, absent = 0, true
			select {
			case <-c.wake:
				continue
			case <-ctx.Done():
				continue
			}
		}
		c.tel.exit(ctx, reason)
		c.setState(StateConnecting)
		if err == nil { // it was up: the next try starts the backoff afresh
			failures, absent = 0, false
		}
		failures++
		t := time.NewTimer(backoff(failures))
		select {
		case <-t.C:
		case <-c.wake:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
		}
	}
}

func backoff(n int) time.Duration {
	hi := backoffBase << min(n, 7)
	lo := hi / 2
	if hi > backoffMax {
		hi = backoffMax
		lo = backoffMax / 2
	}
	return lo + rand.N(hi-lo+1) //nolint:gosec // jitter, not a secret
}

// attach makes one attach and, once up, waits for it to end. It returns
// the exit reason with a nil error when the client was up, or an error
// (ErrTooOld, errAbsent, or why the attach failed) when it never was.
func (c *Client) attach(ctx context.Context) (string, error) {
	c.attempts.Add(1)
	c.setState(StateConnecting)
	if err := checkVersion(ctx, c.opts.Runner, c.caller, c.opts.Program); err != nil {
		return reasonAttachFailed, err
	}
	session, err := lowestSession(ctx, c.opts.Runner, c.caller, c.opts.Program, c.opts.Socket, c.opts.Server == "default")
	if err != nil {
		return reasonAttachFailed, err
	}
	cn, err := startConn(ctx, c.opts.Program, c.opts.Socket, session, attachFlags, c.opts.Parser, c.tee)
	if err != nil {
		return reasonAttachFailed, err
	}
	go cn.read(c.notify(cn))
	info, err := c.handshake(ctx, cn)
	if err != nil {
		cn.close(reasonAttachFailed)
		if errors.Is(err, ErrTooOld) {
			return reasonTooOld, err
		}
		return reasonAttachFailed, err
	}
	c.mu.Lock()
	c.conn, c.session, c.startID = cn, session, info.startID
	subs := append([]Subscription(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		if _, err := cn.do(ctx, s.command(), NoID); err != nil {
			c.detach(cn, reasonAttachFailed)
			return reasonAttachFailed, fmt.Errorf("tmux: re-subscribe: %w", err)
		}
	}
	c.tel.attached(ctx, info.version)
	c.setState(StateUp)
	select {
	case <-cn.done:
		reason := cn.why()
		c.detach(cn, reason)
		return reason, nil
	case <-ctx.Done():
		c.detach(cn, reasonCancelled)
		return reasonCancelled, nil
	}
}

// detach drops cn as the attached client and ends it.
func (c *Client) detach(cn *conn, reason string) {
	c.mu.Lock()
	if c.conn == cn {
		c.conn = nil
	}
	c.mu.Unlock()
	cn.close(reason)
}

// notify wraps the caller's Notify with the client's own bookkeeping: the
// first value of each subscription on this attach is logged as confirmed.
func (c *Client) notify(cn *conn) func(Message) {
	confirmed := map[string]bool{}
	return func(m Message) {
		if s, ok := m.(SubscriptionChanged); ok && !confirmed[s.Name] && c.registered(s.Name) {
			confirmed[s.Name] = true
			c.tel.subscriptionConfirmed(s.Name)
		}
		if c.opts.Notify != nil {
			c.opts.Notify(m)
		}
	}
}

// Wake asks a client waiting in StateAbsent (or in its backoff) to try
// now. 7d calls it when the socket folder changes. It never blocks.
func (c *Client) Wake() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// State returns the client's state.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Up reports whether the client is attached.
func (c *Client) Up() bool { return c.State() == StateUp }

// WaitState waits until the client is in state s or ctx ends.
func (c *Client) WaitState(ctx context.Context, s State) error {
	for {
		c.mu.Lock()
		now, ch := c.state, c.changed
		c.mu.Unlock()
		if now == s {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) setState(s State) {
	c.mu.Lock()
	if c.state == s {
		c.mu.Unlock()
		return
	}
	c.state = s
	close(c.changed)
	c.changed = make(chan struct{})
	c.mu.Unlock()
	c.tel.up(s == StateUp)
}

// ServerStartID is the PaneKey.server_start_id of the server the client
// last attached to ("" before the first attach): minted from the server's
// pid and start time, so a restarted server gets a new one.
func (c *Client) ServerStartID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startID
}

// Session is the session the client is attached to (NoID before the first
// attach).
func (c *Client) Session() SessionID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// ControlDown counts the clients that are not attached: the
// AgentHealth.tmux_control_down value (observed, not asked).
func ControlDown(clients ...*Client) uint32 {
	var n uint32
	for _, c := range clients {
		if !c.Up() {
			n++
		}
	}
	return n
}
