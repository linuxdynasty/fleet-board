package conn

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// A send that finds the stream gone drains what the receiver still holds
// so it can end; a Bye among it is kept, and the stream's failure becomes
// the Bye's outcome, not a reconnect.
func TestASendAtTheStreamsEndKeepsTheByeItDrains(t *testing.T) {
	t.Parallel()
	s := &session{
		l: &Loop{log: slog.New(slog.DiscardHandler)}, msgs: make(chan *agentv1.ConnectResponse, receiveAhead),
		done: make(chan struct{}), rerr: errors.New("the stream ended"),
	}
	s.msgs <- &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_ResultAck{ResultAck: &agentv1.ResultAck{}}}
	s.msgs <- &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
		Bye: &agentv1.Bye{Reason: agentv1.ByeReason_BYE_REASON_REVOKED},
	}}
	s.msgs <- &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Settings{Settings: &agentv1.Settings{}}}
	go func() {
		for len(s.msgs) > 0 { // the end comes after the receiver handed all over
			runtime.Gosched()
		}
		close(s.done)
	}()
	err := s.drainToEnd()
	if !errors.Is(err, s.rerr) {
		t.Fatalf("drainToEnd = %v, want the stream's error", err)
	}
	out, step, err := s.byeOver(context.Background(), ending{}, "outbound", err)
	if out.end != EndRevoked || err != nil || step != "" {
		t.Errorf("byeOver = %v, %q, %v; want the loop ended as revoked", out, step, err)
	}
}

// A Bye the receiver read ahead of the stream's end answers the Hello even
// when awaitAck first looks after both are there: ready together, the Bye
// wins every time (50 tries, where a coin flip would lose about half).
func TestAByeBeforeTheEndAnswersTheHello(t *testing.T) {
	t.Parallel()
	for i := range 50 {
		s := &session{
			l:    &Loop{log: slog.New(slog.DiscardHandler), opts: LoopOptions{HelloTimeout: time.Minute}},
			msgs: make(chan *agentv1.ConnectResponse, receiveAhead), done: make(chan struct{}),
			rerr: errors.New("the stream ended"),
		}
		s.msgs <- &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
			Bye: &agentv1.Bye{Reason: agentv1.ByeReason_BYE_REASON_REVOKED},
		}}
		close(s.done)
		ack, out, err := s.awaitAck(context.Background())
		if ack != nil || err != nil || out.end != EndRevoked {
			t.Fatalf("try %d: awaitAck = %v, %v, %v; want the loop ended as revoked", i, ack, out, err)
		}
	}
}
