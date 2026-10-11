package events

import (
	"context"
	"fmt"
	"slices"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
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
	// gen is the Attach this cursor came from.
	gen uint64
}

// Attach starts a stream's cursor once its snapshot and unacked results are
// out (03n "Ordering"). It drops the ring entries at or below floor, the
// HelloAck.hub_last_event_seq the hub holds (02i rule 2), and from now on
// keeps SHED events for the stream. Detach it when the stream ends; one
// cursor at a time: a new Attach counts as shed what the earlier stream
// left queued, and the earlier cursor then takes and detaches nothing.
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
	if n := len(e.shed); n > 0 { // left by a stream that never detached
		e.dropShed(n)
	}
	e.attached, e.shed = true, nil
	e.gen++
	last, gen := e.last, e.gen
	e.mu.Unlock()
	if size.Events > 0 {
		e.log.InfoContext(ctx, "ring.replay", "from_seq", floor+1, "to_seq", max(last, snapshotSeq), "n", size.Events)
	}
	e.wake()
	return &Cursor{e: e, after: floor, replayBelow: snapshotSeq, gen: gen}, nil
}

// Next returns up to limit events above the last one sent, in seq order,
// and whether the ring may hold more. A limit below 1, or a cursor that a
// newer Attach replaced, returns nothing. It never returns an event while a
// lower seq is still being committed, so the wire order is seq order. The
// cursor counts the returned events as sent.
//
// The ring rows read and the queued SHED events are walked together in seq
// order. When the page of rows is full, nothing above its last row is
// handled (a ring row past it was not read yet). The walk stops once limit
// events are taken; from there on nothing is sent, skipped or counted, and
// the cursor stays below it: SHED events go back on the queue, rows are read
// again. A row that can never be sent (above the message cap, or one that
// does not decode as its own seq) is deleted and counted as loss once, when
// the walk reaches it.
func (c *Cursor) Next(ctx context.Context, limit int) ([]*agentv1.Event, bool, error) {
	e := c.e
	if limit < 1 {
		return nil, false, nil
	}
	e.mu.Lock()
	if c.gen != e.gen {
		e.mu.Unlock()
		return nil, false, nil // a newer Attach owns the queue now
	}
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
	if more {
		bound = rows[len(rows)-1].Seq + 1 // rows above it are not read yet
	}
	out := make([]*agentv1.Event, 0, limit)
	var lost []uint64
	ri, si := 0, 0
	for len(out) < limit {
		takeRow := ri < len(rows) && (si >= len(shed) || rows[ri].Seq < shed[si].GetSeq())
		switch {
		case takeRow:
			r := rows[ri]
			ri++
			c.after = max(c.after, r.Seq)
			if ev, kind := decode(r); kind != "" {
				e.log.WarnContext(ctx, "ring.unreadable", "seq", r.Seq, "err_kind", kind)
				lost = append(lost, r.Seq)
			} else {
				out = append(out, ev)
			}
		case si < len(shed) && shed[si].GetSeq() < bound:
			c.after = max(c.after, shed[si].GetSeq())
			out = append(out, shed[si])
			si++
		default:
			ri = len(rows) // nothing more this call may handle
		}
		if ri == len(rows) && (si == len(shed) || shed[si].GetSeq() >= bound) {
			break
		}
	}
	more = more || ri < len(rows) || (si < len(shed) && shed[si].GetSeq() < bound)
	c.unread(shed[si:])
	if len(lost) > 0 {
		e.loss(ctx, 0, lost...)
	}
	replayed := 0
	for _, ev := range out {
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

// decode reads a ring row; kind names why it can never be sent ("oversize"
// above the message cap, "parse" when it does not decode as its own seq).
func decode(r ring.EventRow) (*agentv1.Event, string) {
	if len(r.Body) > protoversion.MaxPartBytes {
		return nil, "oversize"
	}
	ev := &agentv1.Event{}
	if err := proto.Unmarshal(r.Body, ev); err != nil || ev.GetSeq() != r.Seq {
		return nil, "parse"
	}
	return ev, ""
}

// unread puts SHED events taken from the queue back at its front.
func (c *Cursor) unread(shed []*agentv1.Event) {
	if len(shed) == 0 {
		return
	}
	c.e.mu.Lock()
	defer c.e.mu.Unlock()
	if !c.e.attached || c.gen != c.e.gen {
		c.e.dropShed(len(shed)) // the stream they were taken for is gone
		return
	}
	c.e.shed = append(shed, c.e.shed...)
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
// and counted; durable ones stay in the ring for the next stream. A cursor
// that a newer Attach replaced detaches nothing.
func (c *Cursor) Detach() {
	e := c.e
	e.mu.Lock()
	defer e.mu.Unlock()
	if c.gen != e.gen {
		return // a newer Attach owns the queue now
	}
	e.attached = false
	if n := len(e.shed); n > 0 {
		e.shed = nil
		e.dropShed(n)
	}
}
