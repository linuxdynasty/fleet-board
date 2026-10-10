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
// handler's (the ingest loop and the teardown), one reader (the only caller
// of Receive) and one writer (the only caller of Send).
type stream struct {
	machineID, bootID, peer string
	epoch                   uint64
	// beat is the last Heartbeat's receive time in Unix nanoseconds, 0 when
	// none came; memory only (5b persists last_seen once a minute).
	beat atomic.Int64
	out  chan *agentv1.ConnectResponse

	once   sync.Once
	ended  chan struct{} // closed by end
	bye    agentv1.ByeReason
	reason statedb.EndReason
}

func newStream(machineID, bootID, peer string, epoch uint64) *stream {
	return &stream{
		machineID: machineID, bootID: bootID, peer: peer, epoch: epoch,
		out: make(chan *agentv1.ConnectResponse, outQueue), ended: make(chan struct{}),
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

// read is the stream's reader (03f connReader): a Heartbeat only moves the
// in-memory last-seen time, a Bye from the agent ends the stream, anything
// else goes to the ingest loop in arrival order, blocking while the queue
// is full (HTTP/2 flow control then holds the agent). A second Hello ends
// the stream (errSecondHello). err is how reading ended: nil for the
// agent's Bye or a clean close.
func (s *stream) read(bs *connectStream, now func() time.Time, in chan<- *agentv1.ConnectRequest, done chan<- error) {
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
			s.beat.Store(now().UnixNano())
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
		select {
		case in <- m:
		case <-s.ended:
			done <- nil
			return
		}
	}
}

// errSecondHello is a Hello on a stream that already had one.
var errSecondHello = errors.New("gateway: a second Hello on the stream")

// serve runs the accepted stream until it ends and closes it: the writer
// and reader start, each message goes through the handler table, and the
// teardown stops both before the handler returns, so neither touches the
// stream after it. The returned error is the reader's (an oversize message
// is RESOURCE_EXHAUSTED), nil when the stream ended normally.
func (a *agentService) serve(ctx context.Context, bs *connectStream, rc *http.ResponseController, s *stream) error {
	wrote := make(chan struct{})
	readErr := make(chan error, 1)
	in := make(chan *agentv1.ConnectRequest, ingestQueue)
	go s.write(bs, rc, wrote)
	go s.read(bs, a.now, in, readErr)
	unhandled := map[string]bool{}
	var rerr error
	read := false
	for !read {
		select {
		case m := <-in:
			a.handle(ctx, s, m, unhandled)
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
	a.drain(ctx, s, in, unhandled)
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
// row (moving last_seen) unless a newer Hello already closed it as
// displaced. Removing first means a Hello racing this close never sees a
// stream whose row is already closed and counts no false duplicate.
func (a *agentService) closed(ctx context.Context, s *stream) {
	a.streams.remove(s)
	reason := s.endReason()
	live := false
	if reason != statedb.EndDisplaced {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endWait)
		defer cancel()
		var err error
		live, err = statedb.EndConnection(wctx, a.store, s.machineID, s.epoch, reason, a.now().UTC())
		if err != nil {
			a.log.ErrorContext(ctx, "connect.failed", "step", "store", "err_kind", errKind(err), "machine_id", s.machineID,
				"epoch", s.epoch)
		}
	}
	a.log.InfoContext(ctx, "reconnect.closed", "machine_id", s.machineID, "epoch", s.epoch, "reason", string(reason),
		"row_closed", live)
}
