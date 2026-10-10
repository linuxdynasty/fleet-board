package tmux

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
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
	session, err := lowestSession(ctx, c.opts.Runner, c.caller, c.opts.Program, c.opts.Socket, c.opts.Server == "default")
	if err != nil {
		return reasonAttachFailed, err
	}
	cn, err := startConn(ctx, c.opts.Program, c.opts.Socket, session, attachFlags, c.opts.Parser, c.tee)
	if err != nil {
		return reasonAttachFailed, failed("start", err)
	}
	cn.writeTimeout = c.writeTimeout
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
	c.conn, c.session, c.startID = cn, session, info.startID
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
