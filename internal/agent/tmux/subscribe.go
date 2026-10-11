package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
)

// Subscription is one refresh-client -B format subscription: tmux checks
// Format about once a second and sends %subscription-changed when its value
// changes (01 E4, 02h E13).
type Subscription struct {
	// Name is 1 to 32 of [a-z0-9_-]: it starts the %subscription-changed
	// line, so a space would misread it (7c1) and a ":" would end it early.
	Name string
	// What is the target: "" (the attached session), "%*" or "@*" (every
	// pane or window), or one "%N" or "@N". The row-7 ruling fork 5(e)
	// per-window fallback ("@N") was not needed: tmux 3.2a expands the
	// nested #{S:#{W:...}} loop as 3.7c does.
	What string
	// Format is the format to watch: 1 to 1024 printable ASCII bytes with no
	// single quote (it is sent inside one). It comes from code, never from
	// a person or a pane.
	Format string
}

const maxSubFormat = 1024

var (
	subNameRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	subWhatRE   = regexp.MustCompile(`^(|%\*|@\*|%[0-9]{1,10}|@[0-9]{1,10})$`)
	subFormatRE = regexp.MustCompile(`^[\x20-\x26\x28-\x7e]+$`)
)

func (s Subscription) valid() bool {
	return subNameRE.MatchString(s.Name) && subWhatRE.MatchString(s.What) &&
		len(s.Format) <= maxSubFormat && subFormatRE.MatchString(s.Format)
}

// command is refresh-client -B 'name:what:format'.
func (s Subscription) command() string {
	return "refresh-client -B '" + s.Name + ":" + s.What + ":" + s.Format + "'"
}

// Subscribe registers s (replacing one of the same name) and, when the
// client is attached, sends it. Every registered subscription is sent again
// after each attach, before the client reports StateUp. The first
// %subscription-changed for it on each attach logs
// tmux.subscription_confirmed; 7f's backup list-clients poll runs until then.
func (c *Client) Subscribe(ctx context.Context, s Subscription) error {
	if !s.valid() {
		return ErrBadSubscription
	}
	c.mu.Lock()
	replaced := false
	for i := range c.subs {
		if c.subs[i].Name == s.Name {
			c.subs[i], replaced = s, true
		}
	}
	if !replaced {
		c.subs = append(c.subs, s)
	}
	// Registered and read in one hold of mu: attach sets conn and copies
	// the list in one hold too, so a subscription is either in that copy
	// or sent here, never neither.
	cn := c.conn
	c.mu.Unlock()
	if cn == nil {
		return nil // sent on the next attach
	}
	r, err := cn.do(ctx, s.command(), NoID)
	if errors.Is(err, errConnClosed) {
		return nil // the next attach sends it
	}
	if err == nil && r.Failed {
		// tmux refused it: keep it out of the list, or every later attach
		// would fail on it.
		c.mu.Lock()
		c.subs = slices.DeleteFunc(c.subs, func(x Subscription) bool { return x == s })
		c.mu.Unlock()
		err = ErrCommandFailed
	}
	if err != nil {
		return fmt.Errorf("tmux: subscribe: %w", err)
	}
	return nil
}

// registered reports whether a subscription of that name is registered.
func (c *Client) registered(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.subs {
		if s.Name == name {
			return true
		}
	}
	return false
}
