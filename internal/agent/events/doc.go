// Package events is the agent's event emitter and its send order
// (docs/specs/02i-delivery-and-sequencing.md, "Queue and ring rules";
// 03n W2, "Ordering").
//
// [Stamp] sets Event.delivery and Event.coalesce_key from each body arm's
// [DELIVERY] tag, generated into delivery_table.go by
// scripts/gen-doc-tables.py. [Emitter.Emit] gives the event the next seq of
// agent.db's one allocator; a NEVER_DROP or COALESCE event is committed to
// the ring (internal/agent/ring) before Emit returns, a SHED one waits in a
// bounded queue only while a stream is attached. The connection loop takes
// one [Cursor] per stream ([Emitter.Attach]): it drops the entries the hub
// holds (the HelloAck floor, then each EventAck when the hub lists
// event.ack) and returns events strictly in seq order, never ahead of a lower
// seq still being committed.
package events
