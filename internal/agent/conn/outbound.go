package conn

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
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
	// as that command's finished result, and is not an unlogged refusal.
	ErrResultNotLogged = errors.New("conn: the result is not the one logged in agent.db")
	// ErrQueueFull means a Send made with a hook's context found the
	// queue full; a hook never waits, because it holds the queue's reader.
	ErrQueueFull = errors.New("conn: the stream's outbound queue is full")
)

// hookKey marks the context the loop hands its hooks ([LoopOptions]).
type hookKey struct{}

// outQueue is one stream's outbound queue. Command goroutines put into
// ch; the stream's sending goroutine (serve) is its only reader. dead is
// closed once, when the stream stops serving; gone, as soon as the stream
// or the run ends, even while the sending goroutine is held in a hook.
type outQueue struct {
	ch      chan *agentv1.ConnectRequest
	dead    chan struct{}
	gone    <-chan struct{}
	blocked atomic.Bool // connect.outbound_blocked was logged for this queue
}

func newOutQueue(gone <-chan struct{}) *outQueue {
	return &outQueue{ch: make(chan *agentv1.ConnectRequest, queueSize), dead: make(chan struct{}), gone: gone}
}

// publish makes q the queue Send puts into; Run's goroutine calls it once
// the stream is Connected.
func (l *Loop) publish(q *outQueue) { l.out.Store(q) }

// unpublish ends q: Send refuses it from now on and a Send waiting on it
// returns ErrNoStream. What is left in it is dropped with its stream.
func (l *Loop) unpublish(q *outQueue) {
	l.out.CompareAndSwap(q, nil)
	close(q.dead)
	l.step("unpublished")
}

// Send queues m for the Connected stream, whose sending goroutine sends it
// after the snapshot and its unacked results (03n "Commands, settings and
// the outbound queue"). m is a CommandAck, CommandProgress, CommandResult
// or SettingsAck ([ErrNotQueueable]). A CommandResult must already be
// logged, unchanged, as its command's result in agent.db, so one lost with
// its stream is re-sent from there; the one exception is a refusal that is
// never logged (fork 4 i): FAILED/EPOCH_MISMATCH or
// REJECTED_BY_POLICY/SEQ_REPLAYED for a command_id the log does not hold
// ([ErrResultNotLogged] otherwise).
//
// With no Connected stream Send returns [ErrNoStream] at once. With a full
// queue it waits for room, the stream's or the run's end ([ErrNoStream])
// or ctx, whichever comes first; but a Send made with a hook's context (or
// one derived from it) never waits: it returns [ErrQueueFull] at once,
// because the hook holds the queue's only reader. Send from a hook never
// waits; check its error. nil means queued, not sent: a message queued as
// its stream ends is dropped with it (fleet_agent_outbound_lost_total).
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
	case <-q.gone:
		return ErrNoStream
	case q.ch <- m:
		return nil
	default:
	}
	hook := ctx.Value(hookKey{}) != nil
	if l.m != nil {
		l.m.outboundBlocked(ctx, kind, hook)
	}
	if q.blocked.CompareAndSwap(false, true) {
		l.log.WarnContext(ctx, "connect.outbound_blocked", "kind", kind, "hook", hook)
	}
	if hook {
		return ErrQueueFull
	}
	select {
	case q.ch <- m:
		return nil
	case <-q.dead:
		return ErrNoStream
	case <-q.gone:
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

// maxRefusalMessage caps the message of an unlogged refusal.
const maxRefusalMessage = 256

// unloggedRefusal is a refusal the executor answers without a log row or
// an ack, because the hub re-issues the same command_id (02-contracts
// "Envelope checks", ruling fork 4 i): FAILED/EPOCH_MISMATCH or
// REJECTED_BY_POLICY/SEQ_REPLAYED, a printable command_id, no detail,
// metadata or missing capability, and a message of at most 256 printable
// bytes. retryable, retry_after, the times and the trace may be set.
func unloggedRefusal(r *agentv1.CommandResult) bool {
	st, why := r.GetStatus(), r.GetReason()
	epoch := st == typev1.Status_STATUS_FAILED && why == typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH
	replay := st == typev1.Status_STATUS_REJECTED_BY_POLICY && why == typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED
	if !epoch && !replay {
		return false
	}
	if !printable(r.GetCommandId().GetValue(), maxIDBytes) || r.GetDetail() != nil || len(r.GetMetadata()) > 0 ||
		r.GetMissingCapability() != nil || len(r.GetMessage()) > maxRefusalMessage {
		return false
	}
	for _, c := range []byte(r.GetMessage()) {
		if c < ' ' || c >= 0x7f {
			return false
		}
	}
	return true
}

// logged checks that r is the finished result agent.db holds for its
// command (ruling fork 12 b: committed before it is queued), or an
// unlogged refusal for a command_id the log does not hold.
func (l *Loop) logged(ctx context.Context, r *agentv1.CommandResult) error {
	id := r.GetCommandId().GetValue()
	e, err := l.opts.DB.Command(ctx, id)
	if errors.Is(err, ring.ErrUnknownCommand) {
		if unloggedRefusal(r) {
			return nil
		}
		return ErrResultNotLogged
	}
	if err != nil {
		return fmt.Errorf("conn: check the result is logged: %w", err)
	}
	if e.Status != ring.StatusFinished {
		return fmt.Errorf("%w: the command has no result yet", ErrResultNotLogged)
	}
	var want agentv1.CommandResult
	if err := proto.Unmarshal(e.Result, &want); err != nil {
		args := []any{"err_kind", "parse"}
		if printable(id, maxIDBytes) {
			args = append(args, "command_id", id)
		}
		l.log.WarnContext(ctx, "command.result_unreadable", args...)
		return fmt.Errorf("%w: the logged result cannot be read", ErrResultNotLogged)
	}
	if !proto.Equal(&want, r) {
		return fmt.Errorf("%w: it differs from the logged one", ErrResultNotLogged)
	}
	return nil
}

// serveQueue opens this stream's queue once it is Connected, before the
// status says so, with s.hctx, the hooks' context, which ends with the
// stream or the run (ctx). The returned func, called when the stream stops
// serving, moves the status out of Connected (to backoff, which Run sets
// next unless it stops or ends), ends the queue, counts what was left in
// it and then runs OnDisconnected, so Connected always means Send takes
// messages. The hooks run on Run's goroutine, the stream's only
// sender.
func (s *session) serveQueue(ctx context.Context) func() {
	l := s.l
	hctx, cancel := context.WithCancel(context.WithValue(s.sctx, hookKey{}, true))
	unhook := context.AfterFunc(ctx, cancel)
	s.hctx = hctx
	s.q = newOutQueue(hctx.Done())
	l.publish(s.q)
	return func() {
		l.status.state(StateBackoff, "")
		unhook()
		cancel()
		l.unpublish(s.q)
		// A lower bound: a Send racing the end may still add one.
		if n := len(s.q.ch); n > 0 {
			l.m.outboundLost(ctx, n)
		}
		if f := l.opts.OnDisconnected; f != nil {
			f()
		}
	}
}

// onConnected runs OnConnected once the stream is Connected and its queue
// open: after the snapshot and its unacked results.
func (s *session) onConnected() {
	if f := s.l.opts.OnConnected; f != nil {
		defer s.watchHook("on_connected")()
		f(s.hctx, s.epoch, s.l.HubClock())
	}
}

// step reports a queue step to the test hook (LoopOptions.stepHook).
func (l *Loop) step(name string) {
	if l.opts.stepHook != nil {
		l.opts.stepHook(name)
	}
}

// flush sends what the queue holds now, in order, without waiting for
// more. A requested snapshot flushes after it read the unacked results and
// before it is built, so an ack or progress queued before such a result
// never reaches the hub after the snapshot re-sends it, and nothing lands
// between the snapshot and its results.
func (s *session) flush() error {
	for n := len(s.q.ch); n > 0; n-- {
		if err := s.send(<-s.q.ch); err != nil {
			return err
		}
	}
	return nil
}
