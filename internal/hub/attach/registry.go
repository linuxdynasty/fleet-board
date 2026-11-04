package attach

import (
	"errors"
	"io"
	"sync"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Registry holds one fleetd process's tickets and attach bridges, shared
// by the FleetService handlers and the agent gateway.
type Registry struct {
	mu      sync.Mutex
	now     func() time.Time
	tickets *Store
	bridges map[string]*Bridge
}

// New builds a registry. now is the hub clock; random fills ticket
// secrets (nil reads crypto/rand).
func New(now func() time.Time, random io.Reader) *Registry {
	return &Registry{
		now: now, tickets: NewStore(now, random),
		bridges: map[string]*Bridge{},
	}
}

// MintTicket mints one single-use ticket; see [Store.Mint].
func (r *Registry) MintTicket(in MintIn) (string, time.Time, error) {
	return r.tickets.Mint(in)
}

// BurnTicket burns the ticket the first open frame carried; see
// [Store.Burn].
func (r *Registry) BurnTicket(secret, principalID, sessionID, origin string, readOnlyWanted bool) (*Ticket, error) {
	return r.tickets.Burn(secret, principalID, sessionID, origin, readOnlyWanted)
}

// RevokeTicket un-mints a ticket whose writer job rolled back; see
// [Store.RevokeTicket].
func (r *Registry) RevokeTicket(secret, principalID, requestID string) {
	r.tickets.RevokeTicket(secret, principalID, requestID)
}

// Create registers a pending attach. A duplicate id refuses: one
// attach_id scopes exactly one Terminal stream (02-contracts). The caller
// removes the bridge when the attach ends.
func (r *Registry) Create(attachID, machineID, sessionID string, readOnly bool) (*Bridge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bridges[attachID]; ok {
		return nil, errors.New("attach: duplicate attach id")
	}
	b := &Bridge{
		changed: make(chan struct{}), closed: make(chan struct{}), id: attachID,
		machineID: machineID, sessionID: sessionID, readOnly: readOnly,
	}
	r.bridges[attachID] = b
	return b, nil
}

// Get finds a registered bridge.
func (r *Registry) Get(attachID string) (*Bridge, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bridges[attachID]
	return b, ok
}

// Remove drops a bridge from the registry. It is idempotent: both ends of
// an attach remove in their cleanup, and only the registered instance
// goes. Live bridges stay registered until someone closes them.
func (r *Registry) Remove(b *Bridge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.bridges[b.id]; ok && cur == b {
		delete(r.bridges, b.id)
	}
}

// OpenOrAdopt creates the pending bridge a fresh open needs, or adopts
// the bridge an earlier open of the same request made. The claim and the
// creation are one ordering (tickets before bridges), so concurrent
// double-opens cannot both run: exactly one wins. conflict reports a
// request_id reused for another body. ErrOpenRaced reports an adopt that
// lost its bridge to a close between claim and creation: the caller
// fails its open and the client retries the same request fresh. The
// caller removes the bridge and forgets the open when its open
// transaction fails.
func (r *Registry) OpenOrAdopt(principalID, requestID string, hash [32]byte, attachID, machineID, sessionID string, readOnly bool) (*Bridge, bool, error) {
	r.tickets.mu.Lock()
	defer r.tickets.mu.Unlock()
	existing, fresh, conflict := r.tickets.claimOpenLocked(principalID, requestID, hash, attachID)
	if conflict {
		return nil, false, ErrConflict
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !fresh {
		b, ok := r.bridges[existing]
		if !ok {
			return nil, false, ErrOpenRaced
		}
		return b, true, nil
	}
	b := &Bridge{
		changed: make(chan struct{}), closed: make(chan struct{}), id: attachID,
		machineID: machineID, sessionID: sessionID, readOnly: readOnly,
	}
	r.bridges[attachID] = b
	return b, false, nil
}

// ErrOpenRaced is an adopt that lost its bridge to a close: the client
// retries the same request and opens fresh.
var ErrOpenRaced = errors.New("attach: open raced a close, retry")

// ForgetOpen drops the open record; see [Store.ForgetOpen].
func (r *Registry) ForgetOpen(principalID, requestID, attachID string) {
	r.tickets.ForgetOpen(principalID, requestID, attachID)
}

// CloseAll ends every registered bridge with the reason: hub shutdown
// (HUB_CLOSED) closes browsers at once instead of hanging them.
func (r *Registry) CloseAll(reason typev1.AttachCloseReason) {
	r.mu.Lock()
	var all []*Bridge
	for _, b := range r.bridges {
		all = append(all, b)
	}
	r.bridges = map[string]*Bridge{}
	r.mu.Unlock()
	for _, b := range all {
		b.Close(reason, nil)
	}
}

// CloseMachine closes every bridge on a machine whose agent is gone, so
// its browsers hear AGENT_OFFLINE instead of hanging.
func (r *Registry) CloseMachine(machineID string, reason typev1.AttachCloseReason) {
	r.mu.Lock()
	var doomed []*Bridge
	for _, b := range r.bridges {
		if b.MachineID() == machineID {
			doomed = append(doomed, b)
		}
	}
	for _, b := range doomed {
		b.Close(reason, nil)
		delete(r.bridges, b.id)
	}
	r.mu.Unlock()
}

// CloseCode maps an attach close reason to its 02f WebSocket close code.
// ERROR maps to 1011 here; a refusal with a typed reason code travels in
// AttachClosed first and the socket then closes 1008 (policy) or 1013
// (retryable); the caller picks those from the ErrorDetail.
func CloseCode(reason typev1.AttachCloseReason) int {
	switch reason {
	case typev1.AttachCloseReason_ATTACH_CLOSE_REASON_IDLE_TIMEOUT,
		typev1.AttachCloseReason_ATTACH_CLOSE_REASON_SESSION_ENDED,
		typev1.AttachCloseReason_ATTACH_CLOSE_REASON_DETACHED:
		return 1000
	case typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_SHUTDOWN,
		typev1.AttachCloseReason_ATTACH_CLOSE_REASON_HUB_CLOSED:
		return 1001
	case typev1.AttachCloseReason_ATTACH_CLOSE_REASON_AGENT_OFFLINE:
		return 1013
	default:
		return 1011
	}
}
