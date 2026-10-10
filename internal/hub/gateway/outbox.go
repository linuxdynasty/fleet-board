package gateway

import (
	"context"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// Outbox is the hub's command path. A caller that enqueued commands
// (outbox.Enqueue in its writer job) hands the committed Ops to its
// Notify.
func (s *Server) Outbox() *outbox.Service { return s.connect.outbox }

// Close closes the listener at once (http.Server.Close: no Bye) and stops
// the command sender, waiting for it.
func (s *Server) Close() error {
	s.connect.stop()
	err := s.Server.Close()
	<-s.connect.outboxDone
	return err
}

// startOutbox runs the command sender until the listener stops
// (beginShutdown or Close).
func (d *connectDeps) startOutbox() {
	go func() {
		defer close(d.outboxDone)
		d.outbox.Run(d.stopping)
	}()
}

// stopOutbox waits, within ctx, for the command sender to end and stops
// its gauges. The listener must be stopping already.
func (d *connectDeps) stopOutbox(ctx context.Context) {
	d.stop()
	select {
	case <-d.outboxDone:
		d.outbox.Close()
	case <-ctx.Done():
	}
}

// ready is the seam the Snapshot transaction (plan row 5c) calls once it
// committed: machineID's pending commands may now go out on epoch's
// stream (row 5 ruling, fork 1; 03n "Ordering and consistency"). It is
// false, and nothing is sent, unless epoch is the machine's live stream.
func (d *connectDeps) ready(machineID string, epoch uint64) bool {
	if e, ok := d.streams.liveEpoch(machineID); !ok || e != epoch {
		return false
	}
	d.outbox.Ready(machineID, epoch)
	return true
}

// liveEpoch is the epoch of machineID's live stream.
func (r *registry) liveEpoch(machineID string) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.streams[machineID]; s != nil {
		return s.epoch, true
	}
	return 0, false
}

// Send is outbox.Streams: m goes to machineID's live stream only when that
// stream's epoch is epoch, so a command never travels on another epoch
// than its seq's. It never blocks; the lock is held for the map lookup
// only.
func (r *registry) Send(machineID string, epoch uint64, m *agentv1.ConnectResponse) outbox.SendResult {
	r.mu.Lock()
	s := r.streams[machineID]
	r.mu.Unlock()
	if s == nil || s.epoch != epoch || s.isEnded() {
		return outbox.SendNotLive
	}
	if s.send(m) {
		return outbox.SendQueued
	}
	if s.isEnded() {
		return outbox.SendNotLive
	}
	return outbox.SendFull
}

func (s *stream) isEnded() bool {
	select {
	case <-s.ended:
		return true
	default:
		return false
	}
}

// queue is send that waits while the queue is full, until the stream or
// ctx ends.
func (s *stream) queue(ctx context.Context, m *agentv1.ConnectResponse) bool {
	if s.isEnded() {
		return false
	}
	select {
	case s.out <- m:
		return true
	case <-s.ended:
		return false
	case <-ctx.Done():
		return false
	}
}

// commandWait bounds the store write of an agent's command message. It
// does not end with the stream: a result the hub took is stored even when
// its stream ends meanwhile (the agent re-sends it otherwise).
const commandWait = endWait

// from is the outbox's view of the stream a message came on.
func (a *agentService) from(s *stream) outbox.From {
	return outbox.From{MachineID: s.machineID, Epoch: s.epoch, Live: a.streams.isLive(s)}
}

func commandCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), commandWait)
}
