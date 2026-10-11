package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// DefaultShedQueue is how many SHED events wait for the stream before the
// oldest is dropped (the outbound queue's room, ruling row 6 fork 12 a).
const DefaultShedQueue = 256

// ErrClosed means Emit after Close.
var ErrClosed = errors.New("events: the emitter is closed")

// Options are the inputs of [New].
type Options struct {
	// DB is agent.db; the emitter makes the one live seq allocator on it.
	DB        *ring.DB
	Telemetry *telemetry.Telemetry
	// Caps bound the ring; zero is ring.DefaultCaps.
	Caps ring.Caps
	// ShedQueue bounds the SHED events waiting for the stream; zero is
	// DefaultShedQueue.
	ShedQueue int
	// Now is the agent clock for observed_time and the ring's age cap; nil
	// is time.Now.
	Now func() time.Time
}

// Emitter stamps every event (seq, delivery, coalesce_key), keeps NEVER_DROP
// and COALESCE events in the ring before anyone can send them, and holds
// SHED events in a bounded queue while a stream is attached (02i "Queue and
// ring rules"). It is safe for concurrent use.
//
// Lock: mu, held to allocate a seq and queue the ring job (so jobs, and so
// commits, run in seq order), to read or change the shed queue and the
// pending list, never across a commit wait or a send. Seqs.Next may write
// agent.db once per 1024 seqs while mu is held.
type Emitter struct {
	db      *ring.DB
	seqs    *ring.Seqs
	caps    ring.Caps
	shedCap int
	now     func() time.Time
	log     *slog.Logger
	m       *metrics
	ready   chan struct{}
	events  atomic.Uint64
	bytes   atomic.Uint64
	wg      sync.WaitGroup

	mu sync.Mutex
	// last is the highest seq this emitter handed out; pending the durable
	// seqs queued for the ring and not committed yet, in seq order.
	last     uint64
	pending  []uint64
	shed     []*agentv1.Event
	attached bool
	// shedTotal is shed_total including drops not yet saved; flushing says
	// a goroutine is saving it.
	shedTotal   uint64
	flushing    bool
	closed      bool
	afterSubmit func(seq uint64)
}

// New makes the emitter and the seq allocator of agent.db.
func New(ctx context.Context, opts Options) (*Emitter, error) {
	if opts.DB == nil || opts.Telemetry == nil {
		return nil, errors.New("events: New needs agent.db and telemetry")
	}
	if opts.Caps == (ring.Caps{}) {
		opts.Caps = ring.DefaultCaps
	}
	if opts.ShedQueue <= 0 {
		opts.ShedQueue = DefaultShedQueue
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	seqs, err := opts.DB.Seqs(ctx)
	if err != nil {
		return nil, err
	}
	st, err := opts.DB.State(ctx)
	if err != nil {
		return nil, err
	}
	size, err := opts.DB.EventsSize(ctx)
	if err != nil {
		return nil, err
	}
	e := &Emitter{
		db: opts.DB, seqs: seqs, caps: opts.Caps, shedCap: opts.ShedQueue, now: opts.Now,
		log: opts.Telemetry.Logger.With("component", "events"), ready: make(chan struct{}, 1), shedTotal: st.Marks.Shed,
		// Every seq an earlier run handed out is at or below the stored mark.
		last: st.Marks.SeqHWM,
	}
	e.storeSize(size)
	if e.m, err = newMetrics(opts.Telemetry, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Seqs is the allocator every seq of this agent.db comes from (Snapshot.seq
// included).
func (e *Emitter) Seqs() *ring.Seqs { return e.seqs }

// Size is what the ring holds: AgentHealth.ring_events and ring_bytes.
func (e *Emitter) Size() ring.Size { return ring.Size{Events: e.events.Load(), Bytes: e.bytes.Load()} }

// Ready signals, coalesced, that the attached stream may have more to send.
func (e *Emitter) Ready() <-chan struct{} { return e.ready }

// Emit stamps ev and hands it on by its class. A NEVER_DROP or COALESCE
// event is committed to the ring before Emit returns (the producer waits on
// agent.db, never on the stream); a SHED event never waits and never
// touches the ring. It returns the event's seq.
func (e *Emitter) Emit(ctx context.Context, ev *agentv1.Event) (uint64, error) {
	if err := Stamp(ev); err != nil {
		return 0, err
	}
	now := e.now()
	if ev.GetObservedTime() == nil {
		ev.ObservedTime = timestamppb.New(now)
	}
	ev.Replayed = false
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return 0, ErrClosed
	}
	seq, err := e.seqs.Next(ctx)
	if err != nil {
		e.mu.Unlock()
		return 0, err
	}
	e.last, ev.Seq = seq, seq
	e.m.emitted(ev)
	if ev.GetDelivery() == agentv1.DeliveryClass_DELIVERY_CLASS_SHED {
		e.queueShed(ev)
		e.mu.Unlock()
		e.wake()
		return seq, nil
	}
	body, err := proto.Marshal(ev)
	var wait func() (ring.Appended, error)
	if err == nil {
		wait, err = e.db.SubmitEvent(ctx, ring.EventRow{Seq: seq, Key: ev.GetCoalesceKey(), Created: now, Body: body}, e.caps)
	}
	if err != nil {
		e.mu.Unlock()
		return 0, fmt.Errorf("events: save event %d: %w", seq, err)
	}
	e.pending = append(e.pending, seq)
	hook := e.afterSubmit
	e.mu.Unlock()
	if hook != nil {
		hook(seq)
	}
	res, err := wait()
	e.mu.Lock()
	e.pending = slices.DeleteFunc(e.pending, func(s uint64) bool { return s == seq })
	e.mu.Unlock()
	e.wake()
	if err != nil {
		return 0, fmt.Errorf("events: save event %d: %w", seq, err)
	}
	e.storeSize(res.Size)
	e.m.appended(res)
	if res.Evicted > 0 {
		e.log.Warn("ring.evicted", "n", res.Evicted, "oldest_seq", res.OldestEvicted)
	}
	return seq, nil
}

// queueShed puts a SHED event on the attached stream's queue, dropping the
// oldest past the cap; with no stream it is dropped at once. Call with mu.
func (e *Emitter) queueShed(ev *agentv1.Event) {
	if !e.attached {
		e.dropShed(1)
		return
	}
	e.shed = append(e.shed, ev)
	if n := len(e.shed) - e.shedCap; n > 0 {
		e.shed = slices.Delete(e.shed, 0, n)
		e.dropShed(n)
	}
}

// dropShed counts n shed events and saves shed_total in the background, so
// Hello and Heartbeat, which read agent.db, only report a saved count (a
// count that fell after a crash would look like a rollback). Call with mu.
func (e *Emitter) dropShed(n int) {
	e.shedTotal += uint64(n) //nolint:gosec // G115: n is a queue length
	e.m.shed(n)
	if e.flushing || e.closed {
		return
	}
	e.flushing = true
	e.wg.Go(e.flush)
}

func (e *Emitter) flush() {
	for {
		e.mu.Lock()
		v := e.shedTotal
		e.mu.Unlock()
		_, err := e.db.RaiseMarks(context.Background(), ring.Marks{Shed: v})
		e.mu.Lock()
		if err != nil || e.shedTotal == v {
			e.flushing = false
			e.mu.Unlock()
			if err != nil {
				e.log.Warn("ring.counters_failed", "err_kind", "io")
			}
			return
		}
		e.mu.Unlock()
	}
}

func (e *Emitter) wake() {
	select {
	case e.ready <- struct{}{}:
	default:
	}
}

func (e *Emitter) storeSize(s ring.Size) {
	e.events.Store(s.Events)
	e.bytes.Store(s.Bytes)
}

// Close refuses later events and waits until the shed count is saved. Detach
// the cursor and stop the producers first; close agent.db after.
func (e *Emitter) Close() {
	e.mu.Lock()
	if !e.closed && !e.flushing && e.shedTotal > 0 {
		e.flushing = true
		e.wg.Go(e.flush)
	}
	e.closed = true
	e.mu.Unlock()
	e.wg.Wait()
	_ = e.m.reg.Unregister()
}
