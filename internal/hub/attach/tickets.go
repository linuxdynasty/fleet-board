package attach

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

// TicketTTL is how long a minted ticket burns: 60 s (02f, defaults.md).
const TicketTTL = time.Minute

// Ticket errors: ErrInvalid renders as ATTACH_TICKET_INVALID, ErrDelivered
// as SECRET_ALREADY_DELIVERED (the secret is never re-shown), ErrConflict
// as REQUEST_ID_CONFLICT.
var (
	ErrInvalid   = errors.New("attach: invalid ticket")
	ErrDelivered = errors.New("attach: ticket already delivered")
	ErrConflict  = errors.New("attach: request id used for another request")
)

// MintIn is one ticket mint: who may burn it, for which session, in which
// mode, from which Origin, with the request body for replay checks.
type MintIn struct {
	PrincipalID string
	SessionID   string
	RequestID   string
	ReadOnly    bool
	Origin      string
	Body        []byte
	TTL         time.Duration
}

// Ticket is a burned ticket: the bindings the attach must still honor.
// ReadOnly is the ticket's mode; the open frame may only tighten it.
type Ticket struct {
	PrincipalID string
	SessionID   string
	ReadOnly    bool
	Origin      string
	Expires     time.Time
}

type ticketRec struct {
	Ticket
	hash [32]byte
}

// Store mints and burns single-use attach tickets. A burn deletes the
// ticket whether or not the attach then succeeds (02f): the first frame
// carrying a ticket burns it. A replay of the same request_id never
// re-mints: the same body is already-delivered, another body a conflict.
type Store struct {
	mu       sync.Mutex
	now      func() time.Time
	random   io.Reader
	secrets  map[string]*ticketRec
	requests map[string]string
	hashes   map[string][32]byte
	ttl      time.Duration
	opens    map[string]*openRec
}

// openRec is one AttachOpen the hub accepted: the request body that made
// it and the bridge it opened, so a retried open re-adopts its bridge
// instead of opening twice.
type openRec struct {
	hash     [32]byte
	attachID string
}

// ClaimOpen records the attach one AttachOpen is making and reports what
// an earlier open of the same request did, atomically: concurrent
// double-opens cannot both run. fresh is true when this call won and the
// caller must open attachID; existing names the earlier attach to adopt;
// conflict is true when the request_id already opened another body.
func (s *Store) ClaimOpen(principalID, requestID string, hash [32]byte, attachID string) (existing string, fresh, conflict bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimOpenLocked(principalID, requestID, hash, attachID)
}

// claimOpenLocked is ClaimOpen with s.mu held, so [Registry.OpenOrAdopt]
// can claim the record and create the bridge under one ordering.
func (s *Store) claimOpenLocked(principalID, requestID string, hash [32]byte, attachID string) (existing string, fresh, conflict bool) {
	if s.opens == nil {
		s.opens = map[string]*openRec{}
	}
	key := principalID + "\x00" + requestID
	if rec, ok := s.opens[key]; ok {
		if rec.hash != hash {
			return "", false, true
		}
		// Adopt even our own id: the bridge already stands, and
		// creating a second one under the same id would orphan it.
		return rec.attachID, false, false
	}
	s.opens[key] = &openRec{hash: hash, attachID: attachID}
	return "", true, false
}

// ForgetOpen drops the open record, but only when it still names this
// attach: a newer open for the same request wins.
func (s *Store) ForgetOpen(principalID, requestID, attachID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := principalID + "\x00" + requestID
	if rec, ok := s.opens[key]; ok && rec.attachID == attachID {
		delete(s.opens, key)
	}
}

// NewStore builds a ticket store. now is the hub clock; random fills the
// 256-bit secrets (nil reads crypto/rand). MintIn.TTL bounds one ticket;
// without it every ticket lives TicketTTL.
func NewStore(now func() time.Time, random io.Reader) *Store {
	if random == nil {
		random = rand.Reader
	}
	return &Store{
		now: now, random: random, secrets: map[string]*ticketRec{},
		requests: map[string]string{}, hashes: map[string][32]byte{}, ttl: TicketTTL,
	}
}

// Mint mints one ticket. A repeated (principal, request_id) never mints
// again: the same body is ErrDelivered, another body ErrConflict.
func (s *Store) Mint(in MintIn) (secret string, expires time.Time, err error) {
	ttl := in.TTL
	if ttl <= 0 {
		ttl = s.ttl
	}
	sum := sha256.Sum256(in.Body)
	key := in.PrincipalID + "\x00" + in.RequestID
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked()
	if _, ok := s.requests[key]; ok {
		if s.hashes[key] == sum {
			return "", time.Time{}, ErrDelivered
		}
		return "", time.Time{}, ErrConflict
	}
	var raw [32]byte
	if _, err := io.ReadFull(s.random, raw[:]); err != nil {
		return "", time.Time{}, err
	}
	secret = hex.EncodeToString(raw[:])
	expires = s.now().Add(ttl)
	s.secrets[secret] = &ticketRec{
		Ticket: Ticket{
			PrincipalID: in.PrincipalID, SessionID: in.SessionID,
			ReadOnly: in.ReadOnly, Origin: in.Origin, Expires: expires,
		},
		hash: sum,
	}
	s.requests[key] = secret
	s.hashes[key] = sum
	return secret, expires, nil
}

// Burn burns the ticket the first frame carried: it is deleted whether the
// attach then succeeds or not. An empty session in the open frame matches
// any ticket session (02f: the open session must equal the ticket's, or be
// empty); readOnlyWanted may only tighten the ticket's mode, never widen
// it. Unknown, expired and mis-bound tickets are ErrInvalid.
func (s *Store) Burn(secret, principalID, sessionID, origin string, readOnlyWanted bool) (*Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.secrets[secret]
	delete(s.secrets, secret)
	if !ok {
		return nil, ErrInvalid
	}
	if s.now().After(rec.Expires) {
		return nil, ErrInvalid
	}
	if rec.PrincipalID != principalID || rec.Origin != origin {
		return nil, ErrInvalid
	}
	if sessionID != "" && rec.SessionID != sessionID {
		return nil, ErrInvalid
	}
	out := rec.Ticket
	if readOnlyWanted {
		out.ReadOnly = true
	}
	return &out, nil
}

// RevokeTicket un-mints a ticket whose writer job rolled back, so a
// retry starts clean. Empty secrets are a no-op (the job refused before
// minting).
func (s *Store) RevokeTicket(secret, principalID, requestID string) {
	if secret == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := principalID + "\x00" + requestID
	if s.requests[key] == secret {
		delete(s.requests, key)
		delete(s.hashes, key)
	}
	delete(s.secrets, secret)
}

// purgeLocked drops expired unburned tickets. Burned tickets leave their
// request_id behind on purpose: a replay must still refuse.
func (s *Store) purgeLocked() {
	for secret, rec := range s.secrets {
		if s.now().After(rec.Expires) {
			delete(s.secrets, secret)
		}
	}
}
