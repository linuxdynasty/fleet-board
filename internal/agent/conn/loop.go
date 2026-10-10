package conn

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// DefaultHelloTimeout bounds the wait for HelloAck after Hello
// (defaults.md "HelloAck wait (agent)").
const DefaultHelloTimeout = 30 * time.Second

// LoopOptions are the inputs of [NewLoop].
type LoopOptions struct {
	// Dir is the agent state folder; the cached hub address is read from it
	// before each attempt and written after a connect to another address.
	Dir      string
	Identity Identity
	// DB is agent.db, opened once by the caller under fleet-agent.lock.
	DB      *ring.DB
	Version string
	// BootID is this process's random id, sent in Hello and the facts.
	BootID    string
	Telemetry *telemetry.Telemetry
	// Events carries wake and network-change events (wakenet); nil means
	// none. The loop owns the decision ([wakenet.Decide]).
	Events <-chan wakenet.Event
	// PushOff reports whether the network watcher's push socket is off.
	PushOff func() bool
	// Snapshot fills the parts of a Snapshot that later rows own; nil sends
	// the facts stub. It runs once per snapshot with a context that ends
	// when the stream does.
	Snapshot func(ctx context.Context, s *agentv1.Snapshot) error
	// Heartbeat fills the parts of each A5 Heartbeat that later rows own
	// (load1 and health, row 6; counts, rows 7 and 9; draining; spool;
	// open_attaches); nil sends sent_time, uptime, event_seq and events
	// only, which the loop sets after the hook. It runs on the sending
	// goroutine, so it must only read what it already holds.
	Heartbeat    func(ctx context.Context, hb *agentv1.Heartbeat)
	Backoff      Backoff
	Keepalive    Keepalive
	HelloTimeout time.Duration
	// HandshakeTimeout bounds the TLS handshake of each candidate; zero is
	// 10 s, as for enroll.
	HandshakeTimeout time.Duration
	// Discover browses for the hub (mDNS) once per attempt, after the
	// cached address failed; nil skips it. BrowseTimeout bounds each
	// browse; zero is [DefaultBrowseTimeout].
	Discover      Discover
	BrowseTimeout time.Duration
	// anchorHook, for tests only, sees each stream's hub-time anchor.
	anchorHook func(hubRecv time.Time)
}

// Loop keeps this machine connected to its hub (03 W1 "State machine", 03n W2):
// it tries the cached and configured hub addresses over mTLS, says Hello,
// sends a Snapshot and the unacknowledged results, and reconnects with
// backoff, or at once after a wake or a network change that touched the
// connection (ADR 0029). It ends on its own only when the hub revokes or
// does not know the machine, the hub is another fleet, or the certificate
// expired.
type Loop struct {
	// since is the monotonic time elapsed since t (time.Since); a test
	// replaces it to step the wall clock apart from it.
	since  func(t time.Time) time.Duration
	opts   LoopOptions
	log    *slog.Logger
	tracer trace.Tracer
	m      *loopMetrics
	cert   tls.Certificate
	fp     [32]byte
	seqs   *ring.Seqs
	status statusBox
	// discovery is fleet_agent_discovery_seconds.
	discovery metric.Float64Histogram
	// Only Run's goroutine touches these.
	addrs  []string
	events <-chan wakenet.Event
	// walk is the running attempt's search for the hub.
	walk *walk
}

// NewLoop checks the identity and prepares a loop; nothing is sent until
// [Loop.Run].
func NewLoop(ctx context.Context, opts LoopOptions) (*Loop, error) {
	if opts.Telemetry == nil || opts.DB == nil {
		return nil, errors.New("conn: NewLoop needs telemetry and agent.db")
	}
	cert, err := tls.X509KeyPair([]byte(opts.Identity.CertPEM), []byte(opts.Identity.KeyPEM))
	if err != nil {
		return nil, fmt.Errorf("conn: %s holds no usable certificate and key: %w", IdentityFile, err)
	}
	raw, err := hex.DecodeString(opts.Identity.CAFingerprint)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("conn: %s holds no valid CA fingerprint", IdentityFile)
	}
	seqs, err := opts.DB.Seqs(ctx)
	if err != nil {
		return nil, err
	}
	m, err := newLoopMetrics(opts.Telemetry, opts.Identity.CertNotAfter)
	if err != nil {
		return nil, err
	}
	disc, err := newDiscoveryHistogram(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	opts.Backoff = opts.Backoff.withDefaults()
	opts.Keepalive = opts.Keepalive.withDefaults()
	if opts.HelloTimeout <= 0 {
		opts.HelloTimeout = DefaultHelloTimeout
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = handshakeTimeout
	}
	l := &Loop{
		since: time.Since,
		opts:  opts, log: opts.Telemetry.Logger.With("component", "conn"), tracer: opts.Telemetry.Tracer(scope),
		m: m, cert: cert, fp: [32]byte(raw), seqs: seqs, discovery: disc, events: opts.Events,
	}
	l.addrs = l.configured(ctx)
	l.status.pushOff = opts.PushOff
	return l, nil
}

// Status is a copy of what the loop knows now.
func (l *Loop) Status() Status { return l.status.get() }

// Run connects and stays connected until ctx ends (nil) or the loop ends
// on its own ([EndedError]). Call it once.
func (l *Loop) Run(ctx context.Context) error {
	defer func() { _ = l.m.close() }()
	b := l.opts.Backoff
	p := newPacer(b)
	var last time.Time
	for {
		if !time.Now().Before(l.opts.Identity.CertNotAfter) {
			return l.end(ctx, EndCertExpired, "")
		}
		if !last.IsZero() {
			d, ok := l.pause(ctx, time.Until(last.Add(b.MinGap)), false)
			if !ok {
				return l.stopped()
			}
			if d != nil {
				p.onEvent(d.Reason)
			}
		}
		last = time.Now()
		out := l.attempt(ctx, p.reason)
		switch {
		case ctx.Err() != nil:
			return l.stopped()
		case out.end != "":
			return l.end(ctx, out.end, out.peer)
		case out.connected:
			p.onConnect()
		}
		var wait time.Duration
		if out.event != nil {
			p.onEvent(out.event.Reason)
			wait = b.eventDelay()
		} else {
			wait = p.onFailure()
		}
		l.status.state(StateBackoff, "")
		l.log.InfoContext(ctx, "connect.backoff", "failures", p.failures, "wait_ms", wait.Milliseconds(),
			"reason", reasonLabel(p.reason))
		d, ok := l.pause(ctx, wait, out.event == nil)
		if !ok {
			return l.stopped()
		}
		if d != nil {
			p.onEvent(d.Reason)
			if !sleep(ctx, b.eventDelay()) {
				return l.stopped()
			}
		}
	}
}

// pause waits d while it reads wake and network events. It returns the last
// reconnect decision; with cut, the first one ends the wait. ok is false
// when ctx ended.
func (l *Loop) pause(ctx context.Context, d time.Duration, cut bool) (*wakenet.Decision, bool) {
	t := time.NewTimer(max(d, 0))
	defer t.Stop()
	var got *wakenet.Decision
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-t.C:
			return got, true
		case ev, open := <-l.events:
			if !open {
				l.events = nil
				continue
			}
			if dec, ok := l.decide(ctx, ev, wakenet.Conn{}); ok {
				got = &dec
				if cut {
					return got, true
				}
			}
		}
	}
}

func (l *Loop) decide(ctx context.Context, ev wakenet.Event, c wakenet.Conn) (wakenet.Decision, bool) {
	d := wakenet.Decide(ev, c)
	wakenet.LogDecision(ctx, l.log, ev, d)
	return d, d.Reconnect
}

// ending is how one attempt ended.
type ending struct {
	// connected is true when the stream reached Connected.
	connected bool
	end       End
	// peer is the hub address that ended the loop.
	peer string
	// event is the wake or network change that closed the stream.
	event *wakenet.Decision
}

// candidate is one hub address to try. dial, set for an mDNS answer, is
// the address dialed; addr still names the hub for the TLS check, the
// logs and the cache.
type candidate struct{ addr, dial, source string }

func (c candidate) key() string { return c.addr + " " + c.dial }

// attempt is one cycle through the candidates (03 A2): the cached
// address, then one mDNS browse whose answers are tried as they arrive,
// then the configured addresses (hub-addresses, else identity.json), each
// once. The first whose handshake passes is used, and the attempt ends
// with its stream.
func (l *Loop) attempt(ctx context.Context, reason agentv1.ConnectReason) ending {
	l.status.state(StateDiscovering, "")
	w := l.startWalk(ctx)
	l.walk = w
	defer w.none(ctx)
	seen := map[string]bool{}
	next := func(c candidate) (ending, bool) {
		if seen[c.key()] {
			return ending{}, false
		}
		seen[c.key()] = true
		out, accepted := l.try(ctx, c, reason)
		return out, accepted || ctx.Err() != nil
	}
	if a, err := LoadHubAddress(l.opts.Dir); err == nil {
		if out, done := next(candidate{addr: a, source: "cached"}); done {
			return out
		}
	}
	if l.opts.Discover != nil {
		b := l.startBrowse(w)
		defer b.stop()
		for c := range b.cands {
			if out, done := next(c); done {
				return out
			}
		}
	}
	for _, a := range l.addrs {
		if out, done := next(candidate{addr: a, source: "configured"}); done {
			return out
		}
	}
	if len(seen) == 0 {
		l.log.WarnContext(ctx, "connect.failed", "step", "discover", "err_kind", "constraint")
	}
	return ending{}
}

// end saves an end that needs the owner ([EndedFile]), then ends the loop.
func (l *Loop) end(ctx context.Context, e End, peer string) error {
	l.saveEnd(ctx, e, peer)
	l.status.update(func(s *Status) { s.State, s.Peer, s.Ended = StateEnded, "", e })
	l.log.ErrorContext(ctx, "connect.ended", "state", string(e))
	return &EndedError{End: e}
}

func (l *Loop) stopped() error {
	l.status.state(StateStopped, "")
	return nil
}

// fail records and logs a failed or closed stream.
func (l *Loop) fail(ctx context.Context, c candidate, step string, err error, connected, untrusted bool) {
	kind := connErrKind(err, untrusted)
	l.status.failed(c.addr, Failure{Step: step, ErrKind: kind, Time: time.Now()})
	args := []any{"peer", c.addr, "step", step, "err_kind", kind}
	if ce, ok := errors.AsType[*checkError](err); ok {
		args = append(args, "check", ce.check, "value", ce.value)
	}
	if connected {
		l.log.WarnContext(ctx, "connect.closed", args...)
		return
	}
	l.log.WarnContext(ctx, "connect.failed", args...)
}

// checkError is a HelloAck that failed one of the agent's checks: check
// names it (proto_version, epoch_out_of_range, seq_out_of_range,
// epoch_regressed) and value is the hub's number, both logged with
// err_kind="constraint".
type checkError struct {
	check string
	value uint64
	err   error
}

func (e *checkError) Error() string { return fmt.Sprintf("%v (%s %d)", e.err, e.check, e.value) }

func (e *checkError) Unwrap() error { return e.err }

// connErrKind is the 05b err_kind of a connect failure: a peer that failed
// the handshake or refused the machine is "refused".
func connErrKind(err error, untrusted bool) string {
	var ce *connect.Error
	switch {
	case untrusted:
		return "refused"
	case errors.Is(err, errEpochRegressed), errors.Is(err, errSnapshotOversize), errors.Is(err, errProtoVersion),
		errors.Is(err, ring.ErrOutOfRange):
		return "constraint"
	case errors.Is(err, errNotHelloAck):
		return "parse"
	case errors.As(err, &ce) && (ce.Code() == connect.CodeUnauthenticated ||
		ce.Code() == connect.CodePermissionDenied || ce.Code() == connect.CodeUnimplemented):
		return "refused"
	case errors.As(err, &ce) && ce.Code() == connect.CodeDeadlineExceeded:
		return "timeout"
	}
	return errKind(err)
}

// hubAddresses normalizes a hub's address list (EnrollResponse and
// HelloAck alike): invalid entries are dropped first, then at most 16 are
// kept, in order.
func hubAddresses(in []string) []string {
	out := make([]string, 0, min(len(in), maxHubAddresses))
	for _, a := range in {
		if len(out) == maxHubAddresses {
			break
		}
		if norm, err := HubAddress(a); err == nil {
			out = append(out, norm)
		}
	}
	return out
}

func reasonLabel(r agentv1.ConnectReason) string {
	return strings.ToLower(strings.TrimPrefix(r.String(), "CONNECT_REASON_"))
}

func byeLabel(r agentv1.ByeReason) string { return strings.TrimPrefix(r.String(), "BYE_REASON_") }
