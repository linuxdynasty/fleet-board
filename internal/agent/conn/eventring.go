package conn

import (
	"context"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// eventAckCapability in HelloAck.hub_capabilities says the hub sends
// EventAck (02i "Queue and ring rules"; row 7 ruling fork 1).
const eventAckCapability = "event.ack"

// eventBatch is how many events one turn of serve sends before it looks at
// the hub's messages, the heartbeat and the outbound queue again.
const eventBatch = 64

// eventRate is the stream's event send rate, replay and live alike, in
// bursts of eventBatch (defaults.md "Agent event send rate"; 04 Q7): a
// replay storm from many agents must not fill the hub's ingest queues.
const eventRate = 200

// eventBucket is a token bucket: eventRate a second, at most eventBatch held.
// Only Run's goroutine uses it.
type eventBucket struct {
	tokens float64
	last   time.Time
	// now is time.Now; a test replaces it.
	now func() time.Time
}

func newEventBucket() *eventBucket {
	return &eventBucket{tokens: eventBatch, last: time.Now(), now: time.Now}
}

// take returns how many events may go now (at most limit, and never more
// than the tokens held) and, when none, how long until one may.
func (p *eventBucket) take(limit int) (int, time.Duration) {
	now := p.now()
	p.tokens = min(eventBatch, p.tokens+now.Sub(p.last).Seconds()*eventRate)
	p.last = now
	if p.tokens < 1 {
		return 0, time.Duration((1 - p.tokens) / eventRate * float64(time.Second))
	}
	return min(limit, int(p.tokens)), 0
}

// spend takes n tokens for the events sent.
func (p *eventBucket) spend(n int) { p.tokens -= float64(n) }

// hubLists reports whether the HelloAck lists the hub feature name. The
// agent never uses a feature the hub did not list (02e).
func hubLists(ack *agentv1.HelloAck, name string) bool {
	for _, c := range ack.GetHubCapabilities() {
		if c.GetName() == name && c.GetVersion() >= 1 {
			return true
		}
	}
	return false
}

// attachEvents starts this stream's event cursor once the snapshot and its
// unacked results are out (03n "Ordering"): the ring entries at or below the
// HelloAck floor go, the rest replay in seq order from the next turn of
// serve.
func (s *session) attachEvents(ctx context.Context) error {
	e := s.l.opts.Emitter
	if e == nil {
		return nil
	}
	c, err := e.Attach(ctx, s.floor, s.firstSnap)
	if err != nil {
		return err
	}
	s.cursor, s.pace = c, newEventBucket()
	return nil
}

func (s *session) detachEvents() {
	if s.cursor != nil {
		s.cursor.Detach()
	}
}

// feed signals that the cursor may have events to send; nil (never ready)
// without an emitter.
func (s *session) feed() <-chan struct{} {
	if s.cursor == nil {
		return nil
	}
	return s.l.opts.Emitter.Ready()
}

// sendEvents sends the next events in seq order on Run's goroutine, the
// stream's only sender, as far as the pacer allows; with no token left it
// sets paceWait, and serve calls it again then. A page that leaves more
// re-arms feed.
func (s *session) sendEvents(ctx context.Context) error {
	if s.paceWait != nil {
		return nil
	}
	n, wait := s.pace.take(eventBatch)
	if n == 0 {
		s.paceWait = time.After(wait)
		return nil
	}
	evs, _, err := s.cursor.Next(ctx, n)
	if err != nil {
		return err
	}
	s.pace.spend(len(evs))
	for _, ev := range evs {
		if err := s.send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: ev}}); err != nil {
			return err
		}
	}
	return nil
}

// ackEvents trims what the hub committed (EventAck), only when its HelloAck
// listed event.ack; otherwise the ack is logged and ignored.
func (s *session) ackEvents(ctx context.Context, a *agentv1.EventAck) error {
	if !s.eventAck || s.cursor == nil {
		s.l.log.InfoContext(ctx, "connect.message_ignored", "kind", "event_ack")
		return nil
	}
	return s.cursor.Ack(ctx, a.GetLastEventSeq())
}
