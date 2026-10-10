package conn

import (
	"maps"
	"sync"
	"time"
)

// State is where the connection loop is (03 W1, "State machine").
type State string

const (
	StateDiscovering  State = "discovering"
	StateConnecting   State = "connecting"
	StateHelloSent    State = "hello_sent"
	StateSnapshotting State = "snapshotting"
	StateConnected    State = "connected"
	StateBackoff      State = "backoff"
	StateStopped      State = "stopped"
	StateEnded        State = "ended"
)

// End is why the loop ended on its own. Each needs the owner: enroll again
// (a new token), or look at which hub this machine reached.
type End string

const (
	EndRevoked        End = "revoked"
	EndUnknownMachine End = "unknown_machine"
	EndFleetMismatch  End = "fleet_mismatch"
	EndCertExpired    End = "cert_expired"
)

// EndedError is what [Loop.Run] returns when the loop ended on its own,
// and what [CheckEnded] returns when a run finds its end in [EndedFile].
type EndedError struct {
	End End
	// Recorded is set when the end was read from EndedFile; Peer, Time and
	// Dir then say where and when, for the message.
	Recorded bool
	Peer     string
	Time     time.Time
	Dir      string
}

func (e *EndedError) Error() string {
	if e.Recorded {
		return e.recordedMessage()
	}
	switch e.End {
	case EndRevoked:
		return "conn: the hub revoked this machine; enroll it again with a new token"
	case EndUnknownMachine:
		return "conn: the hub does not know this machine; enroll it again with a new token"
	case EndFleetMismatch:
		return "conn: the hub belongs to another fleet; enroll this machine again to join it"
	case EndCertExpired:
		return "conn: this machine's certificate has expired; enroll it again with a new token"
	default:
		return "conn: the connection loop ended: " + string(e.End)
	}
}

// Failure is the last failure seen for one hub address.
type Failure struct {
	// Step is where it failed: dial, hello, snapshot, stream, ...
	Step string
	// ErrKind is the closed err_kind class of 05b.
	ErrKind string
	Time    time.Time
}

// Status is what the loop knows about its connection, kept in memory.
// internal/agent/statusfile writes it to status.json for `fleet-agent
// doctor` (plan row 6). Times are the agent's wall clock.
type Status struct {
	State State
	// Peer is the hub address of the live stream; empty when not connected.
	Peer string
	// Failures holds the last failure per hub address tried.
	Failures map[string]Failure
	// PushOff is true when the network watcher fell back to polling.
	PushOff bool
	// ClockOffset is hub_time minus the agent's clock at the middle of the
	// last Hello round trip, measured at ClockOffsetTime (zero: none yet).
	ClockOffset     time.Duration
	ClockOffsetTime time.Time
	// Ended is set once the loop ended on its own.
	Ended End
	// SnapshotOversize is the last time a snapshot was over the hub's cap.
	SnapshotOversize time.Time
	// Browse is the last mDNS browse; zero before the first.
	Browse BrowseStatus
}

// statusBox guards the status the loop writes and Status reads.
type statusBox struct {
	mu      sync.Mutex
	s       Status
	pushOff func() bool
}

func (b *statusBox) get() Status {
	b.mu.Lock()
	s := b.s
	s.Failures = maps.Clone(b.s.Failures)
	b.mu.Unlock()
	if b.pushOff != nil {
		s.PushOff = b.pushOff()
	}
	return s
}

func (b *statusBox) update(fn func(s *Status)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(&b.s)
}

func (b *statusBox) state(st State, peer string) {
	b.update(func(s *Status) { s.State, s.Peer = st, peer })
}

func (b *statusBox) failed(addr string, f Failure) {
	b.update(func(s *Status) {
		if s.Failures == nil {
			s.Failures = map[string]Failure{}
		}
		s.Failures[addr] = f
	})
}
