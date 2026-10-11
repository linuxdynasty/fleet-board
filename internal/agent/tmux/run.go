package tmux

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// writeTimeout bounds one command write to tmux's input: a tmux that stops
// reading ends the attach (tmux.exit write_stalled) instead of holding
// every caller.
const writeTimeout = 10 * time.Second

// The wait between attach tries while the server answers has the shape of
// the connection loop's (defaults, "Reconnect backoff"): after n failed
// tries in a row, uniform from 1 s to 2^n s, capped at 60 s, so the first
// wait is 1 to 2 s. An attach that came up resets n.
const (
	backoffBase = time.Second
	backoffMax  = 60 * time.Second
)

func backoff(n int) time.Duration {
	hi := backoffMax
	if n < 6 { // 2^6 s is past the cap
		hi = backoffBase << max(n, 1)
	}
	return backoffBase + rand.N(hi-backoffBase+1) //nolint:gosec // jitter, not a secret
}

// attachError is why an attach failed before it came up: the closed err_kind
// of tmux.exit{reason=attach_failed} (05b).
type attachError struct {
	kind string // version, list, start, handshake, subscribe
	err  error
}

func (e *attachError) Error() string { return "tmux: attach (" + e.kind + "): " + e.err.Error() }
func (e *attachError) Unwrap() error { return e.err }

func failed(kind string, err error) error { return &attachError{kind: kind, err: err} }

// errKind is the err_kind of an attach failure, "" for anything else.
func errKind(err error) string {
	var ae *attachError
	if errors.As(err, &ae) {
		return ae.kind
	}
	return ""
}

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
				c.tel.exit(context.WithoutCancel(ctx), reasonCancelled, "")
			}
			c.setState(StateStopped)
			return ctx.Err()
		case errors.Is(err, ErrTooOld):
			c.tel.exit(ctx, reasonTooOld, "")
			c.setState(StateTooOld)
			return ErrTooOld
		case errors.Is(err, errAbsent):
			if !absent { // logged once per absence, not per wake
				c.tel.exit(ctx, reasonAbsent, "")
			}
			c.setState(StateAbsent)
			failures, absent = 0, true
			select { // no timer: only a Wake (7d's socket watch) or the end
			case <-c.wake:
			case <-ctx.Done():
			}
			continue
		}
		c.tel.exit(ctx, reason, errKind(err))
		c.setState(StateConnecting)
		if err == nil { // it was up: the backoff starts afresh, and a Wake
			// that came while it was up is stale
			failures, absent = 0, false
			select {
			case <-c.wake:
			default:
			}
		}
		failures++
		t := time.NewTimer(c.backoff(failures))
		select {
		case <-t.C:
		case <-c.wake:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
		}
	}
}

// attach makes one attach and, once up, waits for it to end. It returns
// the exit reason with a nil error when the client was up, or an error
// (ErrTooOld, errAbsent, or an *attachError) when it never was.
func (c *Client) attach(ctx context.Context) (string, error) {
	c.attempts.Add(1)
	c.setState(StateConnecting)
	if err := checkVersion(ctx, c.opts.Runner, c.caller, c.opts.Program); err != nil {
		if errors.Is(err, ErrTooOld) {
			return reasonTooOld, err
		}
		return reasonAttachFailed, failed("version", err)
	}
	session, internal, err := lowestSession(ctx, c.opts.Runner, c.caller, c.opts.Program, c.opts.Socket)
	if err != nil {
		return reasonAttachFailed, err
	}
	cn, err := startConn(ctx, c.opts.Program, c.opts.Socket, session, attachFlags, c.opts.Parser, c.tee)
	if err != nil {
		return reasonAttachFailed, failed("start", err)
	}
	cn.writeTimeout = c.writeTimeout
	c.mu.Lock()
	c.expect = session // the attach's own %session-changed is expected
	c.mu.Unlock()
	go cn.read(c.notify(cn))
	info, err := c.handshake(ctx, cn)
	if err != nil {
		cn.close(reasonAttachFailed)
		if errors.Is(err, ErrTooOld) {
			return reasonTooOld, err
		}
		return reasonAttachFailed, failed("handshake", err)
	}
	c.mu.Lock()
	c.conn, c.session, c.startID, c.internal, c.expect = cn, session, info.startID, internal, session
	c.version = info.version
	subs := append([]Subscription(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		r, err := cn.do(ctx, s.command(), NoID)
		if err == nil && r.Failed {
			err = ErrCommandFailed
		}
		if err != nil {
			c.detach(cn, reasonAttachFailed)
			return reasonAttachFailed, failed("subscribe", fmt.Errorf("re-subscribe %s: %w", s.Name, err))
		}
	}
	if internal { // on an internal session only while no developer one exists
		c.repick(ctx, cn)
	}
	go c.repickLoop(ctx, cn)
	c.tel.attached(ctx, info.version, c.onInternal())
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
		switch m := m.(type) {
		case SubscriptionChanged:
			if !confirmed[m.Name] && c.registered(m.Name) {
				confirmed[m.Name] = true
				c.tel.subscriptionConfirmed(m.Name)
			}
		case SessionsChanged: // a token for the re-pick goroutine; a burst coalesces
			select {
			case cn.repick <- struct{}{}:
			default:
			}
		case SessionChanged: // the session this client is on now
			c.mu.Lock()
			unexpected := false
			if c.conn == cn || c.conn == nil {
				// The attach and the client's own switch-client set expect.
				// Anything else is tmux moving the client by itself (with
				// detach-on-destroy off, when its session ends): whether
				// the new one is internal is unknown until a re-pick.
				unexpected = m.Session != c.expect
				c.session, c.expect = m.Session, m.Session
				if unexpected {
					c.internal = true
				}
			}
			c.mu.Unlock()
			if unexpected {
				select {
				case cn.repick <- struct{}{}:
				default:
				}
			}
		}
		if c.opts.Notify != nil {
			c.opts.Notify(m)
		}
	}
}

// repickLoop runs a re-pick for each token the reader drops on
// %sessions-changed, while the client sits on an internal session. It is
// the only goroutine that re-picks after Up, so one list is in flight at a
// time; it ends with the conn. Notify never calls the Client: this
// goroutine does the work.
func (c *Client) repickLoop(ctx context.Context, cn *conn) {
	for {
		select {
		case <-cn.repick:
			if c.onInternal() {
				c.repick(ctx, cn)
			}
		case <-cn.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// repick lists the sessions through the control client and, when a
// developer session exists, moves there with switch-client -E (-E keeps the
// agent's environment out of the developer's session). A failed list or a
// refused switch is logged (tmux.repick_failed, at most once a minute) and
// waits for the next %sessions-changed; the client stays attached.
// Client.mu is held only to read and write the session, never across a
// command.
func (c *Client) repick(ctx context.Context, cn *conn) {
	r, err := cn.do(ctx, "list-sessions -F '"+sessionsFormat+"'", NoID)
	if errors.Is(err, errConnClosed) || ctx.Err() != nil {
		return // the attach is ending: tmux.exit says why
	}
	if err != nil || r.Failed || r.Dropped > 0 {
		c.tel.repickFailed("list")
		return
	}
	body := []byte(strings.Join(r.Body, "\n"))
	marks, err := parseSessions(body)
	if err != nil {
		c.tel.repickFailed("list")
		return
	}
	c.mu.Lock()
	from := c.session // before the switch: %session-changed updates it
	if in, ok := marks[from]; ok && !in && c.conn == cn {
		c.internal = false // already on a developer session (tmux moved it there)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	id, internal, err := pickSession(body)
	if err != nil || internal {
		return // still no developer session: stay, wait for the next notice
	}
	c.mu.Lock()
	c.expect = id // the %session-changed this switch brings is ours
	c.mu.Unlock()
	r, err = cn.do(ctx, "switch-client -E -t "+id.String(), NoID)
	if errors.Is(err, errConnClosed) || ctx.Err() != nil {
		return
	}
	if err != nil || r.Failed {
		c.tel.repickFailed("switch")
		return
	}
	c.mu.Lock()
	if c.conn == cn {
		c.session, c.internal = id, false
	}
	c.mu.Unlock()
	c.tel.sessionSwitched(from, id)
}

// onInternal reports whether the client sits on an internal session.
func (c *Client) onInternal() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.internal
}
