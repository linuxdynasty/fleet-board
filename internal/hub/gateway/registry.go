package gateway

import (
	"sync"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// registry holds the live Connect stream of each machine (03f "What can
// race"). A stream is registered only after its Hello transaction
// committed, and the higher epoch always wins: two Hellos for one machine
// may commit in one batch and their results arrive in either order.
// Entries are removed by stream, never by machine, so the late close of a
// displaced stream cannot remove the stream that displaced it. The lock is
// held only for map work and non-blocking signals, never across a send or
// a store call.
type registry struct {
	mu      sync.Mutex
	streams map[string]*stream
	closing bool // byeAll ran: the hub is shutting down
}

func newRegistry() *registry { return &registry{streams: map[string]*stream{}} }

// registered is how register went.
type registered int

const (
	registeredLive    registered = iota // s is the machine's live stream
	registeredLost                      // a stream with a higher epoch holds the machine
	registeredClosing                   // the hub is shutting down
)

// live copies what the registry knows of machineID's live stream, for
// statedb.HelloIn.Live; Epoch 0 when it holds none.
func (r *registry) live(machineID string) statedb.LiveStream {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.streams[machineID]
	if s == nil {
		return statedb.LiveStream{}
	}
	return statedb.LiveStream{Epoch: s.epoch, LastHeartbeat: s.lastHeartbeat()}
}

// register makes s its machine's live stream unless a stream with a higher
// epoch holds the machine or the hub is shutting down. The stream it
// replaces, if any, is told to end with Bye{DUPLICATE_IDENTITY} and is
// returned so the caller can log and count the displacement.
func (r *registry) register(s *stream) (registered, *stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closing {
		return registeredClosing, nil
	}
	old := r.streams[s.machineID]
	if old != nil && old.epoch > s.epoch {
		return registeredLost, old
	}
	r.streams[s.machineID] = s
	if old != nil {
		old.end(agentv1.ByeReason_BYE_REASON_DUPLICATE_IDENTITY, statedb.EndDisplaced)
	}
	return registeredLive, old
}

// remove deletes s if it is still its machine's live stream.
func (r *registry) remove(s *stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streams[s.machineID] == s {
		delete(r.streams, s.machineID)
	}
}

// isLive reports whether s is its machine's live stream.
func (r *registry) isLive(s *stream) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.streams[s.machineID] == s
}

// get is machineID's live stream, or nil.
func (r *registry) get(machineID string) *stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.streams[machineID]
}

// cut ends machineID's live stream of epoch, if the registry holds it, with
// no Bye and the stored end reason, and cancels its context (row 5 ruling,
// fork 4: a transport close, no new ByeReason). It returns the stream it
// cut, or nil.
func (r *registry) cut(machineID string, epoch uint64, reason statedb.EndReason) *stream {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.streams[machineID]
	if s == nil || s.epoch != epoch {
		return nil
	}
	s.end(agentv1.ByeReason_BYE_REASON_UNSPECIFIED, reason)
	s.cancel()
	return s
}

// cancel ends machineID's live stream, if the registry holds one, with bye
// (BYE_REASON_UNSPECIFIED sends none) and the stored end reason.
func (r *registry) cancel(machineID string, bye agentv1.ByeReason, reason statedb.EndReason) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.streams[machineID]
	if s == nil {
		return false
	}
	s.end(bye, reason)
	return true
}

// byeAll is the shutdown broadcast (http.Server.RegisterOnShutdown): every
// live stream ends with Bye{HUB_RESTART}, and a Hello that commits from now
// on is answered the same way.
func (r *registry) byeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closing = true
	for _, s := range r.streams {
		s.end(agentv1.ByeReason_BYE_REASON_HUB_RESTART, statedb.EndHubRestart)
	}
}
