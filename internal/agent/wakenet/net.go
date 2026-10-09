package wakenet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

const (
	// quietWait: after a hint, read once no further hint came for this long.
	quietWait = 500 * time.Millisecond
	// hintCap: never wait longer than this after the first hint of a burst.
	hintCap = 2 * time.Second
	// pollEvery is the fallback poll, used only while the push socket is off.
	pollEvery = 15 * time.Second
	// readRetry: with the push socket on, a failed read is tried again this
	// long after it, then every readRetryEvery until one works.
	readRetry      = 2 * time.Second
	readRetryEvery = 15 * time.Second
	// pushBuffer holds one read of the push socket.
	pushBuffer = 64 << 10
)

// Net is the network-change watcher. A routing push message is only a hint:
// after a burst of hints settles, it reads the interfaces, addresses and
// default routes and reports an Event only when they differ from the last
// reading. With the push socket off it polls every 15 s instead.
type Net struct {
	Clock  Clock
	Reader Reader
	// OpenPush opens the routing push socket (macOS PF_ROUTE, Linux NETLINK_ROUTE).
	OpenPush func() (io.ReadCloser, error)
	// Hint reports whether one read of the push socket could change the reading.
	Hint func([]byte) bool
	Log  *slog.Logger

	pushOff atomic.Bool
}

// NewNet returns a network watcher on this system's push socket and reader.
func NewNet(log *slog.Logger) *Net {
	return &Net{Clock: SystemClock(), Reader: SystemReader(), OpenPush: openPush, Hint: pushHint, Log: log}
}

// PushOff reports whether, in the current or last Run, the push socket could
// not be opened or stopped working, so the watcher polls every 15 s (for
// fleet-agent doctor).
func (n *Net) PushOff() bool { return n.pushOff.Load() }

// Run watches until ctx ends and sends one Event per confirmed change to out.
// One Run at a time per Net.
//
// With the push socket on, a failed read is tried again 2 s later and then
// every 15 s until one succeeds or a new hint arrives, so the change behind the
// hint is not lost.
func (n *Net) Run(ctx context.Context, out chan<- Event) {
	p := n.startPush(ctx)
	defer p.stop()

	var w netState
	var poll, fire, retry <-chan time.Time
	if p.hints == nil {
		n.compare(ctx, &w, sourceStart, out)
		poll = n.Clock.After(pollEvery)
	} else if !n.compare(ctx, &w, sourceStart, out) {
		retry = n.Clock.After(readRetry)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-p.hints:
			if !open {
				p.hints, retry = nil, nil
				p.stop()
				n.turnPushOff(ctx, p.err)
				poll = n.Clock.After(pollEvery)
				continue
			}
			retry = nil
			now := n.Clock.Now().Mono
			w.hint(now)
			fire = n.Clock.After(w.due() - now)
		case <-fire:
			if now := n.Clock.Now().Mono; now < w.due() {
				fire = n.Clock.After(w.due() - now)
				continue
			}
			fire = nil
			if !n.compare(ctx, &w, SourcePush, out) && p.hints != nil {
				retry = n.Clock.After(readRetry)
			}
		case <-retry:
			retry = nil
			if !n.compare(ctx, &w, SourcePush, out) && p.hints != nil {
				retry = n.Clock.After(readRetryEvery)
			}
		case <-poll:
			n.compare(ctx, &w, SourcePoll, out)
			poll = n.Clock.After(pollEvery)
		}
	}
}

// netState is the last good reading and the debounce window of the hints
// since the last read.
type netState struct {
	base        Reading
	haveBase    bool
	first, last time.Duration
	hints       int
	failures    int // reads failed in a row
}

func (w *netState) hint(now time.Duration) {
	if w.hints == 0 {
		w.first = now
	}
	w.last = now
	w.hints++
}

// due is when the read happens: 500 ms after the last hint, at most 2 s after
// the first.
func (w *netState) due() time.Duration { return min(w.last+quietWait, w.first+hintCap) }

// compare reads the network and sends an Event when it differs from the last
// good reading. It reports whether the read worked. A failed read keeps the
// last good reading; the first of a run of failures is logged, and the
// recovery with the count. src is sourceStart for the reading taken at start.
func (n *Net) compare(ctx context.Context, w *netState, src Source, out chan<- Event) bool {
	hints := w.hints
	w.hints = 0
	cur, err := n.Reader.Read()
	if err != nil {
		w.failures++
		if w.failures == 1 {
			n.Log.WarnContext(ctx, "netchange.read_failed", "source", string(src), "err", err.Error())
		}
		return false
	}
	if w.failures > 0 {
		n.Log.InfoContext(ctx, "netchange.read_recovered", "source", string(src), "failures", w.failures)
		w.failures = 0
	}
	if !w.haveBase {
		w.base, w.haveBase = cur, true
		n.Log.DebugContext(ctx, "netchange.baseline", "source", string(src), "interfaces", len(cur.Interfaces))
		return true
	}
	changes := Diff(w.base, cur)
	if len(changes) == 0 {
		n.Log.DebugContext(ctx, "netchange.unchanged", "source", string(src), "hints", hints)
		return true
	}
	prev := w.base
	w.base = cur
	n.Log.InfoContext(ctx, "netchange.detected", "source", string(src), "hints", hints,
		"changes", len(changes), "kinds", changeKinds(changes), "interfaces", changeIfaces(changes))
	ev := Event{
		Reason: agentv1.ConnectReason_CONNECT_REASON_NETWORK_CHANGE,
		Net:    NetInfo{Source: src, Changes: changes, Previous: prev, Current: cur.clone()},
	}
	select {
	case out <- ev:
	case <-ctx.Done():
	}
	return true
}

func (n *Net) turnPushOff(ctx context.Context, err error) {
	if !n.pushOff.CompareAndSwap(false, true) {
		return
	}
	msg := "closed"
	if err != nil {
		msg = err.Error()
	}
	n.Log.WarnContext(ctx, "netchange.push_off", "err", msg, "poll_s", int(pollEvery/time.Second))
}

// push is the running push socket: hints gets one value per read that holds a
// hint (coalesced) and is closed, after err is set, when the socket dies. stop
// closes the socket and waits for its reader; it may be called more than once.
type push struct {
	hints chan struct{}
	err   error
	stop  func()
}

// startPush opens the push socket and starts its reader. With the socket off
// it returns a push with nil hints, which the watcher treats as "poll".
func (n *Net) startPush(ctx context.Context) *push {
	n.pushOff.Store(false)
	p := &push{stop: func() {}}
	sock, err := n.OpenPush()
	if err != nil {
		n.turnPushOff(ctx, err)
		return p
	}
	hints := make(chan struct{}, 1)
	p.hints = hints
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(hints)
		buf := make([]byte, pushBuffer)
		for {
			k, err := sock.Read(buf)
			switch {
			case errors.Is(err, syscall.ENOBUFS):
				// The kernel dropped messages; something may have changed.
			case err != nil:
				p.err = err
				return
			case !n.Hint(buf[:k]):
				continue
			}
			select {
			case hints <- struct{}{}:
			default:
			}
		}
	}()
	var once sync.Once
	p.stop = func() {
		once.Do(func() { _ = sock.Close() })
		<-done
	}
	return p
}

func changeKinds(cs []Change) []string {
	var out []string
	for _, c := range cs {
		if k := string(c.Kind); !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

func changeIfaces(cs []Change) []string {
	var out []string
	for _, c := range cs {
		if c.Iface != "" && !slices.Contains(out, c.Iface) {
			out = append(out, c.Iface)
		}
	}
	return out
}
