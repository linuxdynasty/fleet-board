package conn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/wakenet"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// shutdownWait bounds the wait for the hub to close the stream after the
// agent's Bye{SHUTDOWN}.
const shutdownWait = 2 * time.Second

var (
	errEpochRegressed = errors.New("conn: the hub's epoch is not above the last one")
	errProtoVersion   = errors.New("conn: the hub chose a protocol version outside this agent's range")
	errNotHelloAck    = errors.New("conn: the hub's first message was not HelloAck")
	errHelloTimeout   = fmt.Errorf("conn: no HelloAck in time: %w", context.DeadlineExceeded)
)

// session is one Connect stream. Run's goroutine sends; one receiver
// goroutine hands the hub's messages over a channel.
type session struct {
	l      *Loop
	k      *link
	c      candidate
	reason agentv1.ConnectReason

	bs     *connect.BidiStreamForClient[agentv1.ConnectRequest, agentv1.ConnectResponse]
	sctx   context.Context
	cancel context.CancelFunc
	msgs   chan *agentv1.ConnectResponse
	done   chan struct{}
	rerr   error // set before done closes
	// serving is set once the stream is Connected.
	serving atomic.Bool

	// applied is Snapshot.last_applied_command_seq: the previous
	// connection's value until the first snapshot is sent.
	applied  uint64
	maxBytes int
	// beat is the heartbeat interval this stream's HelloAck chose.
	beat time.Duration
	// hubAt is this stream's HelloAck.hub_time (the agent's clock when it
	// had none) and hubRecv when the HelloAck arrived, with its monotonic
	// reading: see hubNow.
	hubAt, hubRecv time.Time
}

// try runs one candidate. accepted is true when its handshake passed, so
// the attempt ends here.
func (l *Loop) try(ctx context.Context, c candidate, reason agentv1.ConnectReason) (ending, bool) {
	l.status.state(StateConnecting, "")
	l.log.InfoContext(ctx, "connect.attempt", "peer", c.addr, "source", c.source, "reason", reasonLabel(reason))
	k, err := newLink(c.addr, l.fp, l.cert, l.opts.Keepalive, l.opts.HandshakeTimeout)
	if err != nil {
		l.m.attempt(ctx, "failed", c.source)
		l.fail(ctx, c, "dial", err, false, false)
		return ending{}, false
	}
	defer k.close()
	s := &session{l: l, k: k, c: c, reason: reason}
	out, step, err := s.run(ctx)
	if out.end != "" {
		out.peer = c.addr
	}
	untrusted, handshook := k.untrusted.Load(), k.handshook.Load()
	if ctx.Err() != nil {
		// A stop is neither a failed attempt nor a closed stream.
		return out, handshook && !untrusted
	}
	if err != nil && step != "state" && !handshook {
		// The Hello was queued, but the connection never got past TLS.
		step = "dial"
	}
	if !out.connected {
		result := "failed"
		if untrusted {
			result = "untrusted"
		}
		l.m.attempt(ctx, result, c.source)
	}
	if err != nil {
		l.fail(ctx, c, step, err, out.connected, untrusted)
	}
	return out, handshook && !untrusted
}

// run returns how the stream ended and, on an error, the step it failed in.
func (s *session) run(ctx context.Context) (ending, string, error) {
	s.sctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	// The stream outlives a stop only for the Bye: before Connected it is
	// cut at once, after it shutdownWait later, so a send that HTTP/2 flow
	// control holds (a hub that stopped reading) cannot hold up the stop.
	// A cancel alone does not wake a send waiting for flow control once the
	// response has started; closing the connection does.
	// The late cut is stopped when run returns, so it never fires for a
	// stream that ended in time.
	cut := func() {
		s.cancel()
		s.k.close()
	}
	var mu sync.Mutex
	var late *time.Timer
	returned := false
	unwatch := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case returned:
		case s.serving.Load():
			late = time.AfterFunc(shutdownWait, func() {
				s.l.log.WarnContext(ctx, "connect.shutdown_cut", "peer", s.c.addr, "wait_ms", shutdownWait.Milliseconds())
				cut()
			})
		default:
			cut()
		}
	})
	defer func() {
		unwatch()
		mu.Lock()
		defer mu.Unlock()
		returned = true
		if late != nil {
			late.Stop()
		}
	}()
	s.msgs, s.done = make(chan *agentv1.ConnectResponse), make(chan struct{})
	s.bs = s.k.client.Connect(s.sctx)
	started := false
	defer func() {
		// A cancel alone does not end a response read while the request
		// side is open; closing it lets the transport see the cancel.
		s.cancel()
		_ = s.bs.CloseRequest()
		if started {
			<-s.done
		}
		_ = s.bs.CloseResponse()
	}()
	st, err := s.l.opts.DB.State(ctx)
	if err != nil {
		return ending{}, "state", err
	}
	s.applied = st.CommandSeq
	hctx, span := s.l.tracer.Start(ctx, "agent.connect.hello")
	sent := time.Now()
	s.l.status.state(StateHelloSent, "")
	serr := s.bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Hello{Hello: s.hello(st, sent)}})
	started = true
	go s.receive()
	if serr != nil && !errors.Is(serr, io.EOF) {
		span.End()
		return ending{}, "hello", serr
	}
	ack, out, err := s.awaitAck(ctx)
	if err == nil && ack != nil {
		out, err = s.accept(hctx, ack, st, sent)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "hello")
	}
	span.End()
	if err != nil || ack == nil || out.end != "" {
		return out, "hello", err
	}
	s.l.status.state(StateSnapshotting, "")
	if err := s.sendSnapshot(ctx, true); err != nil {
		return ending{}, "snapshot", err
	}
	return s.connected(ctx)
}

func (s *session) hello(st ring.State, sent time.Time) *agentv1.Hello {
	return &agentv1.Hello{
		MachineId: &typev1.MachineId{Value: s.l.opts.Identity.MachineID}, AgentVersion: s.l.opts.Version,
		ProtoMin: protoversion.Min, ProtoMax: protoversion.Max, BootId: s.l.opts.BootID,
		LastEpoch: st.Epoch, LastAppliedCommandSeq: st.CommandSeq, LastEventSeq: st.Marks.SeqHWM,
		Reason: s.reason, SentTime: timestamppb.New(sent), InstallId: st.InstallID,
		EventCounters: &agentv1.EventCounters{
			ShedTotal: st.Marks.Shed, CoalescedTotal: st.Marks.Coalesced, EvictedTotal: st.Marks.Evicted,
		},
	}
}

// receive hands each message to Run's goroutine until the stream fails,
// then cancels the stream, so a snapshot being built stops.
func (s *session) receive() {
	defer close(s.done)
	for {
		m, err := s.bs.Receive()
		if err != nil {
			s.rerr = err
			s.cancel()
			return
		}
		select {
		case s.msgs <- m:
		case <-s.sctx.Done():
			s.rerr = s.sctx.Err()
			return
		}
	}
}

// awaitAck waits for HelloAck; a Bye instead ends the attempt with its
// outcome.
func (s *session) awaitAck(ctx context.Context) (*agentv1.HelloAck, ending, error) {
	t := time.NewTimer(s.l.opts.HelloTimeout)
	defer t.Stop()
	select {
	case m := <-s.msgs:
		switch b := m.GetBody().(type) {
		case *agentv1.ConnectResponse_HelloAck:
			return b.HelloAck, ending{}, nil
		case *agentv1.ConnectResponse_Bye:
			return nil, s.bye(ctx, b.Bye), nil
		default:
			return nil, ending{}, errNotHelloAck
		}
	case <-s.done:
		return nil, ending{}, s.rerr
	case <-t.C:
		return nil, ending{}, errHelloTimeout
	case <-ctx.Done():
		return nil, ending{}, ctx.Err()
	}
}

// accept checks HelloAck (02i, 02g) and takes its epoch, seq floor, clock
// and hub addresses.
func (s *session) accept(ctx context.Context, ack *agentv1.HelloAck, st ring.State, sent time.Time) (ending, error) {
	recv := time.Now()
	if ack.GetFleetId() != s.l.opts.Identity.FleetID {
		s.goodbye(agentv1.ByeReason_BYE_REASON_FLEET_MISMATCH)
		return ending{end: EndFleetMismatch}, nil
	}
	if v := ack.GetProtoVersion(); v < protoversion.Min || v > protoversion.Max {
		return ending{}, &checkError{check: "proto_version", value: uint64(v), err: errProtoVersion}
	}
	// Both are checked before either is saved: agent.db never lowers them,
	// so one too large would stop every later epoch or seq.
	if err := ring.CheckHubMarks(ack.GetEpoch(), 0); err != nil {
		return ending{}, &checkError{check: "epoch_out_of_range", value: ack.GetEpoch(), err: err}
	}
	if err := ring.CheckHubMarks(0, ack.GetHubLastEventSeq()); err != nil {
		return ending{}, &checkError{check: "seq_out_of_range", value: ack.GetHubLastEventSeq(), err: err}
	}
	if err := s.l.opts.DB.StartEpoch(ctx, ack.GetEpoch()); err != nil {
		if errors.Is(err, ring.ErrEpochNotAbove) {
			s.l.log.WarnContext(ctx, "connect.epoch_regressed", "epoch", ack.GetEpoch(), "last_epoch", st.Epoch)
			return ending{}, &checkError{check: "epoch_regressed", value: ack.GetEpoch(), err: errEpochRegressed}
		}
		return ending{}, err
	}
	if err := s.l.seqs.Floor(ctx, ack.GetHubLastEventSeq()); err != nil {
		return ending{}, err
	}
	var offset time.Duration
	s.hubAt, s.hubRecv = recv, recv
	if hook := s.l.opts.anchorHook; hook != nil {
		hook(s.hubRecv)
	}
	ht := ack.GetHubTime()
	hubTimeOK := ht.IsValid()
	if hubTimeOK {
		s.hubAt = ht.AsTime()
		offset = s.hubAt.Sub(sent.Add(recv.Sub(sent) / 2))
		s.l.status.update(func(ss *Status) { ss.ClockOffset, ss.ClockOffsetTime = offset, recv })
	}
	if addrs := hubAddresses(ack.GetHubAddresses()); len(addrs) > 0 {
		s.l.addrs = addrs
	}
	s.beat = heartbeatInterval(ack.GetHeartbeatInterval())
	s.maxBytes = snapshotMaxBytes
	if mb := int(ack.GetMaxMessageBytes()); mb > 0 && mb < s.maxBytes {
		s.maxBytes = mb
	}
	s.l.log.InfoContext(ctx, "connect.hello", "peer", s.c.addr, "epoch", ack.GetEpoch(), "last_epoch", st.Epoch,
		"last_event_seq", st.Marks.SeqHWM, "hub_last_event_seq", ack.GetHubLastEventSeq(), "boot_id", s.l.opts.BootID,
		"reason", reasonLabel(s.reason), "skew_ms", offset.Milliseconds(), "hub_time_ok", hubTimeOK,
		"heartbeat_ms", s.beat.Milliseconds())
	return ending{}, nil
}

// connected serves the stream until it ends.
func (s *session) connected(ctx context.Context) (ending, string, error) {
	l := s.l
	s.serving.Store(true)
	l.status.state(StateConnected, s.c.addr)
	l.m.connected.Store(1)
	defer l.m.connected.Store(0)
	l.m.attempt(ctx, "connected", s.c.source)
	if s.reason != agentv1.ConnectReason_CONNECT_REASON_START {
		l.m.reconnect(ctx, reasonLabel(s.reason))
	}
	if cached, _ := LoadHubAddress(l.opts.Dir); cached != s.c.addr {
		if err := saveHubAddress(l.opts.Dir, s.c.addr); err != nil {
			l.log.WarnContext(ctx, "connect.failed", "peer", s.c.addr, "step", "save", "err_kind", errKind(err))
		}
	}
	out, step, err := s.serve(ctx)
	out.connected = true
	return out, step, err
}

// serve handles the hub's messages and sends a Heartbeat every s.beat; the
// first comes one interval after the snapshot and its results, already
// sent. A time.Ticker drops ticks for a slow receiver, so a tick missed
// while a send waits is dropped, never queued.
func (s *session) serve(ctx context.Context) (ending, string, error) {
	beat := time.NewTicker(s.beat)
	defer beat.Stop()
	for {
		select {
		case <-beat.C:
			// The stream's context: a stop during the tick still lets this
			// heartbeat finish and the next turn say Bye{SHUTDOWN}.
			if err := s.sendHeartbeat(s.sctx); err != nil {
				return ending{}, "heartbeat", err
			}
		case m := <-s.msgs:
			out, done, step, err := s.handle(ctx, m)
			if err != nil || done {
				return out, step, err
			}
		case <-s.done:
			return ending{}, "stream", s.rerr
		case ev, open := <-s.l.events:
			if !open {
				s.l.events = nil
				continue
			}
			if d, ok := s.l.decide(ctx, ev, wakenet.Conn{Connected: true, LocalAddr: s.k.localAddr()}); ok {
				return ending{event: &d}, "", nil
			}
		case <-ctx.Done():
			s.goodbye(agentv1.ByeReason_BYE_REASON_SHUTDOWN)
			return ending{}, "", nil
		}
	}
}

// handle acts on one message from the hub; done ends the stream.
func (s *session) handle(ctx context.Context, m *agentv1.ConnectResponse) (ending, bool, string, error) {
	switch b := m.GetBody().(type) {
	case *agentv1.ConnectResponse_ResultAck:
		if err := s.ackResults(ctx, b.ResultAck); err != nil {
			return ending{}, true, "result_ack", err
		}
	case *agentv1.ConnectResponse_SnapshotRequest:
		s.l.log.InfoContext(ctx, "snapshot.requested", "reason", clean(b.SnapshotRequest.GetReason(), maxReasonBytes))
		if err := s.sendSnapshot(ctx, false); err != nil {
			return ending{}, true, "snapshot", err
		}
	case *agentv1.ConnectResponse_Bye:
		return s.bye(ctx, b.Bye), true, "", nil
	case *agentv1.ConnectResponse_Command:
		// The executor comes with plan row 6; until then the hub's outbox
		// keeps the command and expires it.
		s.l.log.InfoContext(ctx, "connect.message_ignored", "kind", "command")
	case *agentv1.ConnectResponse_Settings:
		s.l.log.InfoContext(ctx, "connect.message_ignored", "kind", "settings")
	case *agentv1.ConnectResponse_HelloAck:
		s.l.log.InfoContext(ctx, "connect.message_ignored", "kind", "hello_ack")
	default:
		s.l.log.InfoContext(ctx, "connect.message_ignored", "kind", "unknown")
	}
	return ending{}, false, "", nil
}

// bye logs the hub's Bye; REVOKED and UNKNOWN_MACHINE end the loop, any
// other reason is a reconnect with backoff.
func (s *session) bye(ctx context.Context, b *agentv1.Bye) ending {
	r := b.GetReason()
	s.l.log.InfoContext(ctx, "connect.bye", "peer", s.c.addr, "reason", byeLabel(r),
		"message", clean(b.GetMessage(), maxHubMessage))
	switch r {
	case agentv1.ByeReason_BYE_REASON_REVOKED:
		return ending{end: EndRevoked}
	case agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE:
		return ending{end: EndUnknownMachine}
	default:
		return ending{}
	}
}

// goodbye sends Bye (SHUTDOWN when the agent stops, 01 A7) and gives the
// hub a moment to close the stream, draining what it still sends.
func (s *session) goodbye(reason agentv1.ByeReason) {
	err := s.bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Bye{Bye: &agentv1.Bye{Reason: reason}}})
	if err == nil {
		_ = s.bs.CloseRequest()
	}
	t := time.NewTimer(shutdownWait)
	defer t.Stop()
	for {
		select {
		case <-s.msgs:
		case <-s.done:
			return
		case <-t.C:
			return
		}
	}
}
