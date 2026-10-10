package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Per-stream bounds. They are the hub's own buffers, not wire values.
const (
	// outQueue is how many messages wait for the stream's writer.
	outQueue = 16
	// ingestQueue is how many messages the reader hands ahead of the
	// handler table (03f connReader to the ingest channel).
	ingestQueue = 64
	// byeWait bounds a Bye and any send still running at teardown: an agent
	// that stopped reading (a half-open stream, a sleeping laptop) cannot
	// hold the handler, so Shutdown and displacement end in time.
	byeWait = 2 * time.Second
	// endWait bounds the store write that closes the connection row.
	endWait = 5 * time.Second
)

// Connect is a server stream of AgentService.Connect.
type connectStream = connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]

// sender is the stream's send side (connectStream); writeDeadliner its
// write deadline (http.ResponseController). Interfaces so the writer's
// order and deadlines are tested with a stub.
type (
	sender interface {
		Send(*agentv1.ConnectResponse) error
	}
	writeDeadliner interface{ SetWriteDeadline(time.Time) error }
)

// stream is one accepted Connect stream. Three goroutines serve it: the
// handler's (the ingest loop, Heartbeat store jobs and the teardown), one
// reader (the only caller of Receive) and one writer (the only caller of
// Send).
type stream struct {
	machineID, bootID, peer string
	epoch                   uint64
	// hello is what the stream's Hello said that the Snapshot job compares
	// with the store; set before the stream is registered, read only by its
	// ingest goroutine.
	hello helloMarks
	// ctx is the stream's context, given to arm handlers; cancel ends it
	// (a housekeeping cut), so a handler waiting on the store returns.
	ctx    context.Context
	cancel context.CancelFunc
	// beat is the last Heartbeat's receive time (hub clock) in Unix
	// nanoseconds, 0 when none came.
	beat atomic.Int64
	// beatsRead counts the Heartbeats the reader took, stored after beat.
	beatsRead atomic.Uint64
	// stalled is when the reader blocked handing a message to the full
	// ingest queue (hub clock, Unix nanoseconds), 0 while it is not
	// blocked there; a reader waiting in Receive is not stalled (row 5
	// ruling, fork 4).
	stalled atomic.Int64
	// staled is set when housekeeping marked the machine STALE while this
	// stream was its live one: only then does a Heartbeat on it bring the
	// machine back to ONLINE (a STALE from the start sweep waits for the
	// Snapshot, row 5 ruling, fork 1).
	staled atomic.Bool
	// beats wakes the handler goroutine after a Heartbeat (or a STALE
	// mark); one pending wake is enough.
	beats chan struct{}
	// seenWritten is the hub clock of the last last_seen_time write for
	// this stream (the Hello's at first); only the handler goroutine uses it.
	seenWritten time.Time
	in          chan *agentv1.ConnectRequest
	out         chan *agentv1.ConnectResponse

	once   sync.Once
	ended  chan struct{} // closed by end
	bye    agentv1.ByeReason
	reason statedb.EndReason
}

// newStream is the stream of an accepted Hello at now; ctx is the request's.
func newStream(ctx context.Context, machineID, bootID, peer string, epoch uint64, now time.Time) *stream {
	sctx, cancel := context.WithCancel(ctx)
	return &stream{
		machineID: machineID, bootID: bootID, peer: peer, epoch: epoch, ctx: sctx, cancel: cancel,
		beats: make(chan struct{}, 1), seenWritten: now,
		in:  make(chan *agentv1.ConnectRequest, ingestQueue),
		out: make(chan *agentv1.ConnectResponse, outQueue), ended: make(chan struct{}),
	}
}

// wake asks the handler goroutine to look at the stream's Heartbeat state;
// it never blocks.
func (s *stream) wake() {
	select {
	case s.beats <- struct{}{}:
	default:
	}
}

// end asks the stream to end with bye (BYE_REASON_UNSPECIFIED sends none),
// stored as reason. The first call wins; it never blocks.
func (s *stream) end(bye agentv1.ByeReason, reason statedb.EndReason) {
	s.once.Do(func() {
		s.bye, s.reason = bye, reason
		close(s.ended)
	})
}

// endReason is the reason of the first end call; read after ended closed.
func (s *stream) endReason() statedb.EndReason {
	<-s.ended
	return s.reason
}

func (s *stream) lastHeartbeat() time.Time {
	if ns := s.beat.Load(); ns != 0 {
		return time.Unix(0, ns)
	}
	return time.Time{}
}

// send queues m for the writer; false when the queue is full or the stream
// ended. It never blocks.
func (s *stream) send(m *agentv1.ConnectResponse) bool {
	select {
	case <-s.ended:
		return false
	default:
	}
	select {
	case s.out <- m:
		return true
	default:
		return false
	}
}

// write is the stream's writer: queued messages in order, then, once the
// stream ends with a Bye, what is still queued and then the Bye, within
// byeWait.
func (s *stream) write(bs sender, rc writeDeadliner, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case m := <-s.out:
			if err := bs.Send(m); err != nil {
				s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
				return
			}
		case <-s.ended:
			if s.bye == agentv1.ByeReason_BYE_REASON_UNSPECIFIED {
				return
			}
			// What was queued goes first, in order (a HelloAck before the Bye
			// of a stream displaced at once), then the Bye, all within byeWait.
			_ = rc.SetWriteDeadline(time.Now().Add(byeWait))
			for {
				select {
				case m := <-s.out:
					if bs.Send(m) != nil {
						return
					}
				default:
					_ = bs.Send(&agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: &agentv1.Bye{Reason: s.bye}}})
					return
				}
			}
		}
	}
}

// read is the stream's reader (03f connReader): a Heartbeat moves the
// in-memory last-heartbeat time, goes to beat (the clock skew sample) and
// wakes the handler goroutine, which decides any store write; a Bye from
// the agent ends the stream; anything else goes to the ingest loop in
// arrival order, blocking while the queue is full (HTTP/2 flow control then
// holds the agent; the block is the stream's stalled time). A second Hello
// ends the stream (errSecondHello). err is how reading ended: nil for the
// agent's Bye or a clean close.
func (s *stream) read(bs *connectStream, now func() time.Time, beat func(*agentv1.Heartbeat, time.Time), done chan<- error) {
	for {
		m, err := bs.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
			done <- err
			return
		}
		switch m.GetBody().(type) {
		case *agentv1.ConnectRequest_Heartbeat:
			at := now()
			s.beat.Store(at.UnixNano())
			s.beatsRead.Add(1)
			beat(m.GetHeartbeat(), at)
			s.wake()
			continue
		case *agentv1.ConnectRequest_Bye:
			s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndAgentBye)
			done <- nil
			return
		case *agentv1.ConnectRequest_Hello:
			s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, statedb.EndStreamClosed)
			done <- errSecondHello
			return
		}
		if !s.hand(m, now) {
			done <- nil
			return
		}
	}
}

// hand gives m to the ingest queue, marking the stream stalled while the
// queue is full; false when the stream ended first.
func (s *stream) hand(m *agentv1.ConnectRequest, now func() time.Time) bool {
	select {
	case s.in <- m:
		return true
	default:
	}
	s.stalled.Store(now().UnixNano())
	defer s.stalled.Store(0)
	select {
	case s.in <- m:
		return true
	case <-s.ended:
		return false
	}
}

// stalledFor is how long the reader has been stalled at now; 0 when it is
// not.
func (s *stream) stalledFor(now time.Time) time.Duration {
	if ns := s.stalled.Load(); ns != 0 {
		return max(now.Sub(time.Unix(0, ns)), 0)
	}
	return 0
}

// errSecondHello is a Hello on a stream that already had one.
var errSecondHello = errors.New("gateway: a second Hello on the stream")

// serve runs the accepted stream until it ends and closes it: the writer
// and reader start, each message goes through the handler table, and the
// teardown stops both before the handler returns, so neither touches the
// stream after it. The returned error is the reader's (an oversize message
// is RESOURCE_EXHAUSTED), nil when the stream ended normally.
func (a *agentService) serve(ctx context.Context, bs *connectStream, rc *http.ResponseController, s *stream) error {
	defer s.cancel()
	ctx = s.ctx
	wrote := make(chan struct{})
	readErr := make(chan error, 1)
	go s.write(bs, rc, wrote)
	go s.read(bs, a.now, func(h *agentv1.Heartbeat, at time.Time) { a.skewSample(ctx, s.machineID, h.GetSentTime(), at) }, readErr)
	unhandled := map[string]bool{}
	var rerr error
	read := false
	for !read {
		select {
		case m := <-s.in:
			a.handle(ctx, s, m, unhandled)
		case <-s.beats:
			a.heartbeat(ctx, s)
		case rerr = <-readErr:
			read = true
		case <-s.ended:
			_ = rc.SetReadDeadline(time.Now()) // wakes the reader
			rerr = <-readErr
			if s.endReason() != statedb.EndStreamClosed {
				rerr = nil // the hub or the agent ended it; the reader saw only the deadline
			}
			read = true
		}
	}
	// After a housekeeping cut ctx is already cancelled: drained messages
	// reach their arm with a cancelled context, so a store job they submit
	// fails; the agent sends again what the hub never acked.
	a.drain(ctx, s, s.in, unhandled)
	if !a.stop(rc, wrote, byeWait+100*time.Millisecond) {
		// The Bye's deadline must not outlive it: the row close below may
		// take longer, and an expired deadline would reset the stream
		// instead of ending it after the Bye.
		_ = rc.SetWriteDeadline(time.Time{})
	}
	a.closed(ctx, s)
	if errors.Is(rerr, errSecondHello) {
		return a.refuse(ctx, bs, s.peer, s.machineID, helloRefusal{cause: causeSecondHello})
	}
	return rerr
}

// drain handles, in order, what the reader queued before it returned
// (results before the agent's Bye, say): the end of reading never drops a
// message the hub already took.
func (a *agentService) drain(ctx context.Context, s *stream, in <-chan *agentv1.ConnectRequest, seen map[string]bool) {
	for {
		select {
		case m := <-in:
			a.handle(ctx, s, m, seen)
		default:
			return
		}
	}
}

// stop waits for the writer, cutting a send that is still blocked by flow
// control after wait (just over byeWait in serve); cut reports that.
func (a *agentService) stop(rc writeDeadliner, wrote <-chan struct{}, wait time.Duration) (cut bool) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-wrote:
		return false
	case <-t.C:
	}
	_ = rc.SetWriteDeadline(time.Now())
	<-wrote
	return true
}

// closed takes the stream out of the registry, then closes its connection
// row (moving last_seen, and taking an ONLINE or STALE machine OFFLINE in
// the same job) unless a newer Hello already closed it as displaced: a
// displaced stream never changes its machine (#138). Removing first means
// a Hello racing this close never sees a stream whose row is already
// closed and counts no false duplicate.
func (a *agentService) closed(ctx context.Context, s *stream) {
	a.streams.remove(s)
	reason := s.endReason()
	var ended statedb.Ended
	if reason != statedb.EndDisplaced {
		if a.beforeRowClose != nil {
			a.beforeRowClose()
		}
		ctx := context.WithoutCancel(ctx)
		wctx, cancel := context.WithTimeout(ctx, endWait)
		defer cancel()
		var err error
		ended, err = statedb.EndConnection(wctx, a.store, s.machineID, s.epoch, reason, a.now().UTC())
		if err != nil {
			a.log.ErrorContext(ctx, "connect.failed", "step", "store", "err_kind", errKind(err), "machine_id", s.machineID,
				"epoch", s.epoch)
		}
		if ended.Change != nil {
			a.stateChanged(ctx, *ended.Change)
		}
	}
	a.log.InfoContext(ctx, "reconnect.closed", "machine_id", s.machineID, "epoch", s.epoch, "reason", string(reason),
		"row_closed", ended.Live)
}
