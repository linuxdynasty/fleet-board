package gateway

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// The registry hands a command only to the machine's live stream of the
// command's epoch, says when that stream's queue is full, and says not
// live once the stream ended or a newer epoch holds the machine.
func TestRegistrySendNeedsTheLiveEpoch(t *testing.T) {
	t.Parallel()
	r := newRegistry()
	msg := &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Command{Command: &agentv1.Command{}}}
	if got := r.Send("m1", 1, msg); got != outbox.SendNotLive {
		t.Errorf("no stream: %v, want not live", got)
	}
	s1 := newStream("m1", "b", "p", 1)
	r.register(s1)
	if got := r.Send("m1", 2, msg); got != outbox.SendNotLive {
		t.Errorf("another epoch: %v, want not live", got)
	}
	for i := range outQueue {
		if got := r.Send("m1", 1, msg); got != outbox.SendQueued {
			t.Fatalf("send %d: %v, want queued", i, got)
		}
	}
	if got := r.Send("m1", 1, msg); got != outbox.SendFull {
		t.Errorf("a full queue: %v, want full", got)
	}
	s2 := newStream("m1", "b", "p", 2)
	r.register(s2) // displaces s1
	if got := r.Send("m1", 1, msg); got != outbox.SendNotLive {
		t.Errorf("a displaced epoch: %v, want not live", got)
	}
	if got := r.Send("m1", 2, msg); got != outbox.SendQueued {
		t.Errorf("the new epoch: %v, want queued", got)
	}
	s2.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, "")
	if got := r.Send("m1", 2, msg); got != outbox.SendNotLive {
		t.Errorf("an ended stream: %v, want not live", got)
	}
}

// queue waits for room in the stream's queue, and gives up when the stream
// or the context ends.
func TestStreamQueueWaitsForRoom(t *testing.T) {
	t.Parallel()
	s := newStream("m1", "b", "p", 1)
	msg := &agentv1.ConnectResponse{}
	for range outQueue {
		s.out <- msg
	}
	done := make(chan bool)
	go func() { done <- s.queue(context.Background(), msg) }()
	<-s.out // the writer takes one
	select {
	case ok := <-done:
		if !ok {
			t.Error("queue gave up although room came")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("queue did not take the room")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.queue(ctx, msg) {
		t.Error("queue took a full queue with its context done")
	}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, "")
	if s.queue(context.Background(), msg) {
		t.Error("queue took a message on an ended stream")
	}
}
