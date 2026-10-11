package outbox

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// scope names the outbox's tracer and meter.
const scope = "github.com/linuxdynasty/fleet-board/internal/hub/outbox"

// SendResult is how [Streams.Send] went.
type SendResult int

const (
	// SendQueued: the message is in the stream's send queue.
	SendQueued SendResult = iota
	// SendFull: the stream is live at that epoch but its queue is full.
	SendFull
	// SendNotLive: the machine has no live stream at that epoch.
	SendNotLive
)

// Streams is the hub's live Connect streams (the gateway's registry).
type Streams interface {
	// Send queues m on machineID's live stream when that stream's epoch is
	// epoch, without blocking. A command must never reach a stream of
	// another epoch: its seq belongs to the epoch it was issued in.
	Send(machineID string, epoch uint64, m *agentv1.ConnectResponse) SendResult
}

// Options build a [Service]. Store, Streams and Telemetry are required.
type Options struct {
	Store     *store.Store
	Streams   Streams
	Telemetry *telemetry.Telemetry
	// Now is the hub clock; nil means time.Now.
	Now func() time.Time
}

// Service is the hub's command path (03f, 02i "Command lifecycle"): it
// sends pending commands to machines whose Snapshot of the current epoch
// committed ([Service.Ready]), and applies what the agent answers
// ([Service.Ack], [Service.Progress], [Service.Result]). [Enqueue] is how
// a command gets in. One goroutine, [Service.Run], does every send.
type Service struct {
	store   *store.Store
	streams Streams
	now     func() time.Time
	log     *slog.Logger
	tracer  trace.Tracer
	m       *metrics

	mu sync.Mutex // guards ready, dirty; never held across a store call or a send
	// ready is the epoch of each machine whose Snapshot of that epoch
	// committed and whose stream has not ended.
	ready map[string]uint64
	// dirty are the machines the next pass looks at.
	dirty map[string]bool
	wake  chan struct{}

	// sends is owned by Run: what each machine's current epoch was sent.
	sends map[string]*epochSends
}

// epochSends is what one connection epoch of a machine was sent: the last
// seq issued (seqs start at 1 per epoch, 02-contracts) and the command ids
// queued on it, so a send whose mark did not commit is not repeated.
type epochSends struct {
	epoch, seq uint64
	sent       map[string]bool
	// backoff is the wait before the next look after a full queue.
	backoff time.Duration
}

// New builds a Service; call Run to send and Close when done.
func New(opts Options) (*Service, error) {
	if opts.Store == nil || opts.Streams == nil || opts.Telemetry == nil {
		return nil, errors.New("outbox: New needs a store, the streams and telemetry")
	}
	s := &Service{
		store: opts.Store, streams: opts.Streams, now: opts.Now,
		log: opts.Telemetry.Logger, tracer: opts.Telemetry.Tracer(scope),
		ready: map[string]uint64{}, dirty: map[string]bool{}, wake: make(chan struct{}, 1),
		sends: map[string]*epochSends{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	m, err := newMetrics(opts.Telemetry, s.pendingByMachine, s.now)
	if err != nil {
		return nil, err
	}
	s.m = m
	return s, nil
}

// Close stops reporting the outbox gauges. Call it after Run returned.
func (s *Service) Close() { s.m.close() }

// Notify is what a caller of [Enqueue] does once its job committed: each
// new command is logged (outbox.enqueued) and its machine is looked at, so
// a machine that is ready gets it at once. A repeat (Created false) is not
// logged again.
func (s *Service) Notify(ops ...Op) {
	machines := make([]string, 0, len(ops))
	for _, op := range ops {
		if op.Created {
			s.log.Info("outbox.enqueued", "command_id", op.CommandID, "machine_id", op.MachineID, "kind", op.Kind)
		}
		machines = append(machines, op.MachineID)
	}
	s.kick(machines...)
}

// Ready says the Snapshot of machineID's connection epoch committed, so its
// pending commands may go out on that epoch's stream (row 5 ruling, fork
// 1: no command between Hello and Snapshot). An epoch below the one
// already ready is ignored; the same epoch again only looks again.
func (s *Service) Ready(machineID string, epoch uint64) {
	s.mu.Lock()
	if cur, ok := s.ready[machineID]; ok && epoch < cur {
		s.mu.Unlock()
		return
	}
	s.ready[machineID] = epoch
	s.mu.Unlock()
	s.kick(machineID)
}

// Gone says the stream of machineID's epoch ended: nothing more is sent
// for that epoch. A later epoch already ready is left alone.
func (s *Service) Gone(machineID string, epoch uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready[machineID] == epoch {
		delete(s.ready, machineID)
	}
}

// kick marks machines to look at and wakes Run; it never blocks.
func (s *Service) kick(machines ...string) {
	if len(machines) == 0 {
		return
	}
	s.mu.Lock()
	for _, m := range machines {
		s.dirty[m] = true
	}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// take returns the dirty machines that are ready, with their epochs, and
// clears the dirty set.
func (s *Service) take() map[string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]uint64{}
	for m := range s.dirty {
		if e, ok := s.ready[m]; ok {
			out[m] = e
		}
	}
	clear(s.dirty)
	return out
}
