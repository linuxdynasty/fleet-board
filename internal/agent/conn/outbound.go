package conn

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// queueSize is the room of a stream's outbound queue (ruling fork 12 a).
const queueSize = 256

// Errors of [Loop.Send].
var (
	// ErrNoStream means no stream to the hub is Connected, or the one the
	// message waited on ended. The caller keeps what it needs durable: a
	// result is re-sent from agent.db after the next snapshot.
	ErrNoStream = errors.New("conn: no stream to the hub is connected")
	// ErrNotQueueable means the message is not a CommandAck,
	// CommandProgress, CommandResult or SettingsAck.
	ErrNotQueueable = errors.New("conn: only command acks, progress, results and settings acks are queued")
	// ErrResultNotLogged means a CommandResult is not the one agent.db holds
	// as that command's finished result.
	ErrResultNotLogged = errors.New("conn: the result is not the one logged in agent.db")
)

// outQueue is one stream's outbound queue. Command goroutines put into
// ch; the stream's sending goroutine (serve) is its only reader. dead is
// closed once, when the stream stops serving.
type outQueue struct {
	ch   chan *agentv1.ConnectRequest
	dead chan struct{}
}

func newOutQueue() *outQueue {
	return &outQueue{ch: make(chan *agentv1.ConnectRequest, queueSize), dead: make(chan struct{})}
}

// publish makes q the queue Send puts into; Run's goroutine calls it once
// the stream is Connected.
func (l *Loop) publish(q *outQueue) { l.out.Store(q) }

// unpublish ends q: Send refuses it from now on and a Send waiting on it
// returns ErrNoStream. What is left in it is dropped with its stream.
func (l *Loop) unpublish(q *outQueue) {
	l.out.CompareAndSwap(q, nil)
	close(q.dead)
}

// Send queues m for the Connected stream, whose sending goroutine sends it
// after the snapshot and its unacked results (03n "Ordering and
// consistency"). m is a CommandAck, CommandProgress, CommandResult or
// SettingsAck ([ErrNotQueueable]); a CommandResult must already be logged,
// unchanged, as its command's result in agent.db ([ErrResultNotLogged]),
// so one lost with its stream is re-sent from there. With no Connected
// stream Send returns [ErrNoStream] at once; with a full queue it waits
// for room, the stream's end ([ErrNoStream]) or ctx, whichever comes
// first. nil means queued, not sent: a message queued as its stream ends
// is dropped with it. Send never sends on the stream itself.
func (l *Loop) Send(ctx context.Context, m *agentv1.ConnectRequest) error {
	kind, ok := queueKind(m)
	if !ok {
		return ErrNotQueueable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r := m.GetCommandResult(); r != nil {
		if err := l.logged(ctx, r); err != nil {
			return err
		}
	}
	q := l.out.Load()
	if q == nil {
		return ErrNoStream
	}
	select {
	case <-q.dead:
		return ErrNoStream
	case q.ch <- m:
		return nil
	default:
	}
	l.log.WarnContext(ctx, "connect.outbound_blocked", "kind", kind)
	select {
	case q.ch <- m:
		return nil
	case <-q.dead:
		return ErrNoStream
	case <-ctx.Done():
		return ctx.Err()
	}
}

// queueKind is the label of a queueable message; ok is false for any
// other (a snapshot, a heartbeat or a Hello is the stream's own, and an
// event goes through the ring, row 7).
func queueKind(m *agentv1.ConnectRequest) (string, bool) {
	switch {
	case m.GetCommandAck() != nil:
		return "command_ack", true
	case m.GetCommandProgress() != nil:
		return "command_progress", true
	case m.GetCommandResult() != nil:
		return "command_result", true
	case m.GetSettingsAck() != nil:
		return "settings_ack", true
	}
	return "", false
}

// logged checks that r is the finished result agent.db holds for its
// command (ruling fork 12 b: committed before it is queued).
func (l *Loop) logged(ctx context.Context, r *agentv1.CommandResult) error {
	e, err := l.opts.DB.Command(ctx, r.GetCommandId().GetValue())
	if errors.Is(err, ring.ErrUnknownCommand) {
		return ErrResultNotLogged
	}
	if err != nil {
		return fmt.Errorf("conn: check the result is logged: %w", err)
	}
	var want agentv1.CommandResult
	if e.Status != ring.StatusFinished || proto.Unmarshal(e.Result, &want) != nil || !proto.Equal(&want, r) {
		return ErrResultNotLogged
	}
	return nil
}

// serveQueue opens this stream's queue once it is Connected and runs
// OnConnected; the returned func, called when the stream stops serving,
// ends the queue and then runs OnDisconnected. Both hooks run on Run's
// goroutine, the stream's only sender.
func (s *session) serveQueue() func() {
	l := s.l
	s.q = newOutQueue()
	l.publish(s.q)
	if f := l.opts.OnConnected; f != nil {
		f(s.epoch, l.HubClock())
	}
	return func() {
		l.unpublish(s.q)
		if f := l.opts.OnDisconnected; f != nil {
			f()
		}
	}
}
