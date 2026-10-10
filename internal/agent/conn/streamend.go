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
		return s.bye(ctx, b), "", nil
	}
	return out, step, err
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
