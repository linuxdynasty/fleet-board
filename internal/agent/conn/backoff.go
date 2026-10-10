package conn

import (
	"context"
	"math/rand/v2"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Reconnect pacing (defaults.md "Reconnect backoff" and "Random delay before
// the attempt a wake or network change starts").
const (
	DefaultBackoffFirst  = time.Second
	DefaultBackoffMax    = 60 * time.Second
	DefaultConnectGap    = 5 * time.Second
	defaultEventDelayMin = 20 * time.Millisecond
	defaultEventDelayMax = 120 * time.Millisecond
)

// Backoff paces the connection loop. After a failed try the wait is full
// jitter between First and First doubled once per failure since the last
// reset (so the first wait is First to 2*First), capped at Max; two connect attempts are at least MinGap apart; and an
// attempt that a wake or network change starts waits a random
// EventDelayMin..EventDelayMax first. A connect, a wake and a network change
// reset the doubling. Zero fields take the defaults.
type Backoff struct {
	First, Max, MinGap           time.Duration
	EventDelayMin, EventDelayMax time.Duration
}

func (b Backoff) withDefaults() Backoff {
	if b.First <= 0 {
		b.First = DefaultBackoffFirst
	}
	if b.Max <= 0 {
		b.Max = DefaultBackoffMax
	}
	if b.MinGap <= 0 {
		b.MinGap = DefaultConnectGap
	}
	if b.EventDelayMin <= 0 {
		b.EventDelayMin = defaultEventDelayMin
	}
	if b.EventDelayMax <= 0 {
		b.EventDelayMax = defaultEventDelayMax
	}
	return b
}

// wait is the time before the next try after failures (1 or more) failed
// tries in a row: full jitter from First up to First doubled once per
// failure (2 s after the first with the defaults), capped at Max.
func (b Backoff) wait(failures int) time.Duration {
	ceil := b.First
	for i := 0; i < failures && ceil < b.Max; i++ {
		ceil *= 2
	}
	ceil = min(ceil, b.Max)
	if ceil <= b.First {
		return b.First
	}
	return b.First + rand.N(ceil-b.First+1) //nolint:gosec // G404: jitter spreads reconnects; it is not a secret
}

// eventDelay spreads the reconnects of many agents that see the same wake
// or network change.
func (b Backoff) eventDelay() time.Duration {
	if b.EventDelayMax <= b.EventDelayMin {
		return b.EventDelayMin
	}
	return b.EventDelayMin + rand.N(b.EventDelayMax-b.EventDelayMin+1) //nolint:gosec // G404: jitter, not a secret
}

// sleep waits d or until ctx ends; it reports whether the wait finished.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// pacer holds the connection loop's failures in a row and the reason the
// next Hello gives. Every rule that moves them is here, so the backoff
// wait and the minimum gap between tries apply the same one: a failure
// doubles the next wait's ceiling; a connect, a wake or a network change
// resets the doubling (defaults.md "Reconnect backoff").
type pacer struct {
	b        Backoff
	failures int
	reason   agentv1.ConnectReason
}

func newPacer(b Backoff) *pacer {
	return &pacer{b: b, reason: agentv1.ConnectReason_CONNECT_REASON_START}
}

// onFailure counts a failed try (or a stream that closed) and returns the
// wait before the next; that try says RECONNECT unless an event comes.
func (p *pacer) onFailure() time.Duration {
	p.failures++
	p.reason = agentv1.ConnectReason_CONNECT_REASON_RECONNECT
	return p.b.wait(p.failures)
}

// onConnect resets the doubling: the stream reached Connected.
func (p *pacer) onConnect() { p.failures = 0 }

// onEvent resets the doubling for a wake or network change that calls for
// a reconnect; the next try gives its reason.
func (p *pacer) onEvent(r agentv1.ConnectReason) {
	p.failures = 0
	p.reason = r
}
