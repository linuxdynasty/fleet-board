package conn

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// ackOrBye is the stream's first message from the hub: HelloAck, or a Bye
// that ends the attempt with its outcome.
func (s *session) ackOrBye(ctx context.Context, m *agentv1.ConnectResponse) (*agentv1.HelloAck, ending, error) {
	switch b := m.GetBody().(type) {
	case *agentv1.ConnectResponse_HelloAck:
		return b.HelloAck, ending{}, nil
	case *agentv1.ConnectResponse_Bye:
		return nil, s.bye(ctx, b.Bye), nil
	default:
		return nil, ending{}, errNotHelloAck
	}
}

// byeOver turns a stream that failed into the outcome of a Bye the hub
// sent before it ended and the sender had not read: one send drained
// (lastBye) or one the receiver read ahead and left in msgs. Without it a
// Bye{REVOKED} that arrived while a hook held the sender would be lost to
// the stream's end, and the agent would reconnect instead of stopping.
func (s *session) byeOver(ctx context.Context, out ending, step string, err error) (ending, string, error) {
	if err == nil || out.end != "" {
		return out, step, err
	}
	if b := s.leftBye(); b != nil {
		// The failure the Bye stands in for is still said (a Snapshot
		// hook's error, say), then the Bye decides what happens.
		args := []any{"peer", s.c.addr, "step", step, "err_kind", connErrKind(err, false)}
		if s.serving.Load() {
			s.l.log.WarnContext(ctx, "connect.closed", args...)
		} else {
			s.l.log.WarnContext(ctx, "connect.failed", args...)
		}
		return s.bye(ctx, b), "", nil
	}
	return out, step, err
}

// byeFirst is out, unless a Bye the hub sent is waiting: then the Bye's
// outcome, so a wake, a network change or a stop that came at the same
// time never hides a Bye{REVOKED}.
func (s *session) byeFirst(ctx context.Context, out ending) ending {
	if b := s.leftBye(); b != nil {
		return s.bye(ctx, b)
	}
	return out
}

// leftBye is lastBye, else the first Bye left in msgs; the other messages
// are dropped with their stream (the hub re-sends what matters).
func (s *session) leftBye() *agentv1.Bye {
	if s.lastBye != nil {
		return s.lastBye
	}
	for {
		select {
		case m := <-s.msgs:
			if b := m.GetBye(); b != nil {
				return b
			}
		default:
			return nil
		}
	}
}

// drainToEnd waits for the receiver to finish once a send found the
// stream gone, reading what it still hands over so it can reach its end
// (the read-ahead may be full). What the hub sent before the end is moot,
// except a Bye, which is kept for byeOver. It returns the stream's error.
func (s *session) drainToEnd() error {
	for {
		select {
		case m := <-s.msgs:
			if b := m.GetBye(); b != nil && s.lastBye == nil {
				s.lastBye = b
			}
		case <-s.done:
			return s.rerr
		}
	}
}
