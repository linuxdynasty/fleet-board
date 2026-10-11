package events

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
)

// Cursor is one stream's place in the event order. Only the stream's
// sending goroutine uses it.
type Cursor struct {
	e *Emitter
	// after is the highest seq sent on this stream (the HelloAck floor
	// before the first); replayBelow is the first snapshot's seq: an event
	// below it was created before the snapshot and is sent as replayed.
	after, replayBelow uint64
}

// Attach starts a stream's cursor once its snapshot and unacked results are
// out (03n "Ordering"). It drops the ring entries at or below floor, the
// HelloAck.hub_last_event_seq the hub holds (02i rule 2), and from now on
// keeps SHED events for the stream. Detach it when the stream ends; one
// cursor at a time.
func (e *Emitter) Attach(ctx context.Context, floor, snapshotSeq uint64) (*Cursor, error) {
	_, span := e.m.tracer.Start(ctx, "agent.ring.replay")
	defer span.End()
	n, size, err := e.db.TrimEvents(ctx, floor)
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("events: trim to the floor: %w", err)
	}
	e.storeSize(size)
	if n > 0 {
		e.m.dropped("floor", n)
		e.log.InfoContext(ctx, "ring.trimmed", "to_seq", floor, "n", n, "reason", "floor")
	}
	e.mu.Lock()
	e.attached, e.shed = true, nil
	last := e.last
	e.mu.Unlock()
	if size.Events > 0 {
		e.log.InfoContext(ctx, "ring.replay", "from_seq", floor+1, "to_seq", max(last, snapshotSeq), "n", size.Events)
	}
	e.wake()
	return &Cursor{e: e, after: floor, replayBelow: snapshotSeq}, nil
}

// Next returns up to limit events above the last one sent, in seq order,
// and whether the ring may hold more. It never returns an event while a
// lower seq is still being committed, so the wire order is seq order. The
// cursor counts the returned events as sent.
func (c *Cursor) Next(ctx context.Context, limit int) ([]*agentv1.Event, bool, error) {
	e := c.e
	e.mu.Lock()
	bound := e.last + 1
	if len(e.pending) > 0 {
		bound = min(bound, e.pending[0])
	}
	i := 0
	for i < len(e.shed) && e.shed[i].GetSeq() < bound {
		i++
	}
	shed := slices.Clone(e.shed[:i])
	e.shed = slices.Delete(e.shed, 0, i)
	e.mu.Unlock()
	rows, err := e.db.EventsBetween(ctx, c.after, bound, limit)
	if err != nil {
		c.unread(shed)
		return nil, false, fmt.Errorf("events: read the ring: %w", err)
	}
	more := len(rows) == limit
	out := make([]*agentv1.Event, 0, len(rows)+len(shed))
	var lost []uint64
	for _, r := range rows {
		if len(r.Body) > protoversion.MaxPartBytes {
			// Never sendable (stored before the emit guard, or by hand): loss.
			e.log.WarnContext(ctx, "ring.unreadable", "seq", r.Seq, "err_kind", "oversize")
			c.after = max(c.after, r.Seq)
			lost = append(lost, r.Seq)
			continue
		}
		ev := &agentv1.Event{}
		if err := proto.Unmarshal(r.Body, ev); err != nil || ev.GetSeq() != r.Seq {
			e.log.WarnContext(ctx, "ring.unreadable", "seq", r.Seq, "err_kind", "parse")
			c.after = max(c.after, r.Seq) // never sendable; step past it
			continue
		}
		out = append(out, ev)
	}
	if len(lost) > 0 {
		e.loss(ctx, len(lost), lost...)
	}
	out = append(out, shed...)
	slices.SortFunc(out, func(a, b *agentv1.Event) int { return compare(a.GetSeq(), b.GetSeq()) })
	// What is past limit waits for the next call: SHED events go back on
	// the queue, ring rows are read again.
	if len(out) > limit {
		c.unread(slices.DeleteFunc(slices.Clone(out[limit:]), func(ev *agentv1.Event) bool {
			return ev.GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_SHED
		}))
		out, more = out[:limit], true
	}
	replayed := 0
	for _, ev := range out {
		c.after = max(c.after, ev.GetSeq())
		ev.Replayed = ev.GetSeq() < c.replayBelow
		if ev.GetReplayed() {
			replayed++
		}
	}
	e.m.replayed(replayed)
	if more {
		e.wake()
	}
	return out, more, nil
}

// unread puts SHED events taken from the queue back at its front.
func (c *Cursor) unread(shed []*agentv1.Event) {
	if len(shed) == 0 {
		return
	}
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	if !c.e.attached {
		c.e.dropShed(len(shed))
		return
	}
	c.e.shed = append(shed, c.e.shed...)
}

func compare(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Ack drops the ring entries at or below seq that this stream sent: the hub
// committed them (EventAck, 02i; only when HelloAck listed event.ack). An
// ack above what was sent never takes an unsent entry; a repeat or an older
// ack changes nothing.
func (c *Cursor) Ack(ctx context.Context, seq uint64) error {
	to := min(seq, c.after)
	if to == 0 {
		return nil
	}
	n, size, err := c.e.db.TrimEvents(ctx, to)
	if err != nil {
		return fmt.Errorf("events: trim acked events: %w", err)
	}
	c.e.storeSize(size)
	if n > 0 {
		c.e.m.dropped("acked", n)
		c.e.log.DebugContext(ctx, "ring.trimmed", "to_seq", to, "n", n, "reason", "acked")
	}
	return nil
}

// Detach ends the stream's cursor: the SHED events still queued are dropped
// and counted; durable ones stay in the ring for the next stream.
func (c *Cursor) Detach() {
	e := c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attached = false
	if n := len(e.shed); n > 0 {
		e.shed = nil
		e.dropShed(n)
	}
}
