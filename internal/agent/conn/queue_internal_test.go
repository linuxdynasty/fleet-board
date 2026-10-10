package conn

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func ack(id string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandAck{
		CommandAck: &agentv1.CommandAck{CommandId: &typev1.CommandId{Value: id}},
	}}
}

// A Send waiting on a full queue is refused with ErrNoStream when the
// stream ends, and every later Send is refused at once.
func TestASendWaitingOnAFullQueueIsRefusedWhenTheStreamEnds(t *testing.T) {
	t.Parallel()
	l := &Loop{log: slog.New(slog.DiscardHandler)}
	q := newOutQueue(make(chan struct{}))
	l.publish(q)
	ctx := context.Background()
	for range queueSize {
		if err := l.Send(ctx, ack("a")); err != nil {
			t.Fatal(err)
		}
	}
	got := make(chan error, 1)
	go func() { got <- l.Send(ctx, ack("waiting")) }()
	select {
	case err := <-got:
		t.Fatalf("Send into a full queue returned %v without waiting", err)
	case <-time.After(50 * time.Millisecond):
	}
	l.unpublish(q)
	select {
	case err := <-got:
		if !errors.Is(err, ErrNoStream) {
			t.Errorf("the waiting Send = %v, want ErrNoStream", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting Send never returned")
	}
	if err := l.Send(ctx, ack("after")); !errors.Is(err, ErrNoStream) {
		t.Errorf("Send after the stream ended = %v, want ErrNoStream", err)
	}
	if len(q.ch) != queueSize {
		t.Errorf("the dead queue holds %d, want the %d queued before; nothing more", len(q.ch), queueSize)
	}
}

// A context that already ended is refused before anything is queued.
func TestSendWithAnEndedContextQueuesNothing(t *testing.T) {
	t.Parallel()
	l := &Loop{log: slog.New(slog.DiscardHandler)}
	q := newOutQueue(make(chan struct{}))
	l.publish(q)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Send(ctx, ack("a")); !errors.Is(err, context.Canceled) {
		t.Errorf("Send = %v, want context.Canceled", err)
	}
	if len(q.ch) != 0 {
		t.Errorf("the queue holds %d after a refused Send", len(q.ch))
	}
}

// The stream's or the run's end (gone) releases a Send waiting on a full
// queue even while the sending goroutine never returns to unpublish it
// (a hook that waits on Send).
func TestTheStreamsEndReleasesASendWaitingOnAFullQueue(t *testing.T) {
	t.Parallel()
	l := &Loop{log: slog.New(slog.DiscardHandler)}
	gone := make(chan struct{})
	l.publish(newOutQueue(gone))
	ctx := context.Background()
	for range queueSize {
		if err := l.Send(ctx, ack("a")); err != nil {
			t.Fatal(err)
		}
	}
	got := make(chan error, 1)
	go func() { got <- l.Send(ctx, ack("waiting")) }()
	select {
	case err := <-got:
		t.Fatalf("Send into a full queue returned %v without waiting", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(gone)
	select {
	case err := <-got:
		if !errors.Is(err, ErrNoStream) {
			t.Errorf("the waiting Send = %v, want ErrNoStream", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream's end did not release the waiting Send")
	}
}
