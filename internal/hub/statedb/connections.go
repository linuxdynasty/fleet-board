package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// connectionsSQL is migration 5 (plan row 5, PR 5a-i): one machine_connections
// row per accepted Hello, and the machine's last epoch and last_seen time.
// Like cert_serials it has no foreign key to machines, so the history
// outlives DeleteMachine (02g). The partial unique index allows one live row
// (ended_time NULL) per machine (03f race table, "Duplicate Hello").
const connectionsSQL = `ALTER TABLE machines ADD COLUMN last_epoch INTEGER NOT NULL DEFAULT 0
	CHECK (last_epoch BETWEEN 0 AND 9223372036854775806);
ALTER TABLE machines ADD COLUMN last_seen_time INTEGER;
CREATE TABLE machine_connections(
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	epoch INTEGER NOT NULL CHECK (epoch BETWEEN 1 AND 9223372036854775806),
	boot_id TEXT NOT NULL CHECK (boot_id <> ''),
	install_id TEXT NOT NULL CHECK (install_id <> ''),
	peer TEXT NOT NULL CHECK (peer <> ''),
	connect_reason TEXT NOT NULL
		CHECK (connect_reason IN ('unspecified', 'start', 'reconnect', 'wake', 'network_change')),
	started_time INTEGER NOT NULL,
	ended_time INTEGER,
	end_reason TEXT CHECK (end_reason IN
		('displaced', 'stream_closed', 'agent_bye', 'hub_restart', 'ingest_stalled', 'offline', 'revoked')),
	displaced_by INTEGER CHECK (displaced_by > epoch),
	last_heartbeat_age_ms INTEGER CHECK (last_heartbeat_age_ms >= 0),
	PRIMARY KEY (machine_id, epoch),
	CHECK ((ended_time IS NULL) = (end_reason IS NULL)),
	CHECK (ended_time IS NULL OR ended_time >= started_time),
	CHECK ((displaced_by IS NOT NULL) = (end_reason IS 'displaced')),
	CHECK (last_heartbeat_age_ms IS NULL OR displaced_by IS NOT NULL)
) STRICT;
CREATE UNIQUE INDEX machine_connections_live ON machine_connections(machine_id) WHERE ended_time IS NULL;`

// MaxEpoch is the largest connection epoch the hub issues. It equals the
// agent's ring.MaxEpoch, the largest HelloAck.epoch agent.db accepts, so a
// Hello whose next epoch would pass it is refused ([ErrEpochOutOfRange])
// rather than answered with an epoch the agent drops.
const MaxEpoch uint64 = math.MaxInt64 - 1

// Hello refusals other than [ErrMachineUnknown] and [ErrMachineRevoked],
// which mean what they mean for [Renew], and [ErrPeerSerialUnknown], which
// for Hello means only a serial stored for another machine.
var (
	// ErrHelloInvalid: a HelloIn field is empty, too long or not printable
	// ASCII, or its Reason is not a [ConnectReason].
	ErrHelloInvalid = errors.New("statedb: the Hello is malformed")
	// ErrEpochOutOfRange: max(stored epoch, Hello.last_epoch) + 1 would pass
	// [MaxEpoch]. Nothing is written.
	ErrEpochOutOfRange = errors.New("statedb: the next connection epoch is out of range")
	// ErrSerialRevoked: the presented serial is stored with a revoked_time.
	ErrSerialRevoked = errors.New("statedb: the certificate serial is revoked")
	// ErrEndReasonInvalid: EndConnection was given a reason that is not an
	// [EndReason] it may write.
	ErrEndReasonInvalid = errors.New("statedb: invalid connection end reason")
)

// ConnectReason is Hello.reason as stored: the gateway maps the proto enum.
type ConnectReason string

// The stored values of Hello.reason.
const (
	ConnectUnspecified   ConnectReason = "unspecified"
	ConnectStart         ConnectReason = "start"
	ConnectReconnect     ConnectReason = "reconnect"
	ConnectWake          ConnectReason = "wake"
	ConnectNetworkChange ConnectReason = "network_change"
)

// EndReason says why a connection row was closed.
type EndReason string

// The stored end reasons. Only [Hello] writes EndDisplaced.
const (
	EndDisplaced     EndReason = "displaced"      // a newer Hello for the machine won
	EndStreamClosed  EndReason = "stream_closed"  // the transport closed or failed
	EndAgentBye      EndReason = "agent_bye"      // the agent sent Bye
	EndHubRestart    EndReason = "hub_restart"    // the hub sent Bye{HUB_RESTART} on shutdown
	EndIngestStalled EndReason = "ingest_stalled" // housekeeping cut a stream whose ingest stalled
	EndOffline       EndReason = "offline"        // housekeeping cut the stream of an OFFLINE machine
	EndRevoked       EndReason = "revoked"        // the hub sent Bye{REVOKED}
)

// HelloIn is one Hello the gateway accepted at the transport level.
type HelloIn struct {
	// MachineID and PeerSerial come from the verified client certificate,
	// never from the Hello message.
	MachineID  string
	PeerSerial string
	// LastEpoch is Hello.last_epoch.
	LastEpoch uint64
	// BootID and InstallID are Hello.boot_id and Hello.install_id: 1 to 64
	// printable ASCII characters without spaces (the 02g install_id rule).
	BootID    string
	InstallID string
	// Peer is the stream's remote address (host:port), at most 128 bytes.
	Peer   string
	Reason ConnectReason
	// Live is a copy of what the gateway's registry knew of the machine's
	// live stream before it submitted; Epoch 0 means none. It is a value so
	// the caller takes the copy under its own lock: the job reads it on the
	// writer goroutine. Its LastHeartbeat is recorded as the displaced
	// connection's heartbeat age only when Live.Epoch is the epoch of the
	// row this Hello displaces; otherwise the age is unknown.
	Live LiveStream
	Now  time.Time
}

// LiveStream is the gateway's in-memory view of one live stream.
type LiveStream struct {
	Epoch         uint64
	LastHeartbeat time.Time // zero when the stream had no Heartbeat yet
}

// HelloOut is what the Hello transaction committed and what HelloAck needs
// from the store.
type HelloOut struct {
	Epoch uint64
	// HubLastEventSeq is the hub's stored event seq for the machine. Plan
	// row 5c stores it; until then it is 0.
	HubLastEventSeq uint64
	// SettingsGeneration is the machine's resolved settings generation. Plan
	// row 5h stores it; until then it is 0.
	SettingsGeneration uint64
	// SettingsEpoch is hub_identity.settings_epoch.
	SettingsEpoch string
	// Displaced is the connection this Hello displaced, or nil.
	Displaced *Displaced
}

// Displaced is a connection a newer Hello displaced. Its stream gets
// Bye{DUPLICATE_IDENTITY} after commit, if the gateway still holds it.
type Displaced struct {
	Epoch     uint64
	BootID    string
	InstallID string
	Peer      string
	Started   time.Time
	// HeartbeatAge is Now minus the displaced stream's last Heartbeat,
	// stored as last_heartbeat_age_ms; HeartbeatKnown is false (NULL) when
	// the caller's Live did not name this connection or had no Heartbeat.
	HeartbeatAge   time.Duration
	HeartbeatKnown bool
}

var connectReasons = map[ConnectReason]bool{
	ConnectUnspecified: true, ConnectStart: true, ConnectReconnect: true, ConnectWake: true, ConnectNetworkChange: true,
}

// endReasons are the reasons EndConnection writes; EndDisplaced is Hello's.
var endReasons = map[EndReason]bool{
	EndStreamClosed: true, EndAgentBye: true, EndHubRestart: true, EndIngestStalled: true, EndOffline: true, EndRevoked: true,
}

// Hello records an accepted Hello as one writer job, and its result arrives
// only after the commit (02i "Connection epoch", 03n, 03f race table):
//
//   - checks a machine row for MachineID that is not REVOKED
//     ([ErrMachineUnknown], [ErrMachineRevoked]) and that PeerSerial is not
//     revoked ([ErrSerialRevoked]) nor stored for another machine
//     ([ErrPeerSerialUnknown]); a serial the store lacks is accepted;
//   - then sets the epoch to max(machines.last_epoch, LastEpoch) + 1, or
//     refuses with [ErrEpochOutOfRange] past [MaxEpoch];
//   - closes the machine's live machine_connections row, whichever stream
//     it belongs to, as displaced by the new epoch, and returns it in
//     HelloOut.Displaced: possibly a row a hub crash left live, which no
//     stream holds;
//   - inserts the new live row and stores last_epoch and last_seen_time.
//
// Times never step back: an end before the row's start is stored as the
// start, and last_seen_time only moves forward.
//
// It never changes machines.state: the Snapshot transaction marks the
// machine ONLINE (row 5 ruling, fork 1). A malformed HelloIn is
// [ErrHelloInvalid] before anything is submitted. Two Hellos for one
// machine may commit in one batch, so their results can arrive in either
// order: the higher epoch is the live one. A ctx error means the outcome
// is unknown; a row left live is displaced by the machine's next Hello.
func Hello(ctx context.Context, s *store.Store, in HelloIn) (HelloOut, error) {
	if err := in.validate(); err != nil {
		return HelloOut{}, err
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out HelloOut
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = applyHello(jctx, tx, in)
		return err
	})
	if err != nil {
		return HelloOut{}, err
	}
	return out, nil
}

func (in HelloIn) validate() error {
	switch {
	case in.MachineID == "" || in.PeerSerial == "":
		return fmt.Errorf("%w: the certificate's machine id and serial are required", ErrHelloInvalid)
	case !printable(in.BootID, 64):
		return fmt.Errorf("%w: boot_id must be 1 to 64 printable ASCII characters without spaces", ErrHelloInvalid)
	case !printable(in.InstallID, 64):
		return fmt.Errorf("%w: install_id must be 1 to 64 printable ASCII characters without spaces", ErrHelloInvalid)
	case !printable(in.Peer, 128):
		return fmt.Errorf("%w: peer must be 1 to 128 printable ASCII characters without spaces", ErrHelloInvalid)
	case !connectReasons[in.Reason]:
		return fmt.Errorf("%w: unknown connect reason", ErrHelloInvalid)
	}
	return nil
}

// printable is 1 to n bytes of printable ASCII without spaces.
func printable(s string, n int) bool {
	if s == "" || len(s) > n {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

func applyHello(ctx context.Context, tx *sql.Tx, in HelloIn) (HelloOut, error) {
	if err := checkConnectPeer(ctx, tx, in.MachineID, in.PeerSerial); err != nil {
		return HelloOut{}, err
	}
	var out HelloOut
	err := tx.QueryRowContext(ctx, `SELECT settings_epoch FROM hub_identity WHERE id = 1`).Scan(&out.SettingsEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return HelloOut{}, ErrNoIdentity
	}
	if err != nil {
		return HelloOut{}, fmt.Errorf("statedb: read settings epoch: %w", err)
	}
	var stored int64
	if err := tx.QueryRowContext(ctx, `SELECT last_epoch FROM machines WHERE machine_id = ?`, in.MachineID).
		Scan(&stored); err != nil {
		return HelloOut{}, fmt.Errorf("statedb: read last epoch: %w", err)
	}
	prev := max(uint64(stored), in.LastEpoch) //nolint:gosec // G115: last_epoch has CHECK BETWEEN 0 AND MaxEpoch
	if prev >= MaxEpoch {
		return HelloOut{}, fmt.Errorf("%w: max(stored, last_epoch) is %d", ErrEpochOutOfRange, prev)
	}
	out.Epoch = prev + 1
	if out.Displaced, err = displace(ctx, tx, in, out.Epoch); err != nil {
		return HelloOut{}, err
	}
	now := in.Now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO machine_connections(machine_id, epoch, boot_id, install_id, peer,
		connect_reason, started_time) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.MachineID, int64(out.Epoch), in.BootID, in.InstallID, in.Peer, string(in.Reason), now); err != nil { //nolint:gosec // G115: Epoch <= MaxEpoch
		return HelloOut{}, fmt.Errorf("statedb: insert connection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE machines SET last_epoch = ?, last_seen_time = max(ifnull(last_seen_time, 0), ?)
		WHERE machine_id = ?`, int64(out.Epoch), now, in.MachineID); err != nil { //nolint:gosec // G115: Epoch <= MaxEpoch
		return HelloOut{}, fmt.Errorf("statedb: store epoch: %w", err)
	}
	return out, nil
}

// checkConnectPeer is Connect's check (02g "A revoked or deleted machine
// stays out", 03f "Revocation"): a machine row that is not REVOKED, and a
// presented serial that is neither revoked ([ErrSerialRevoked]) nor stored
// for another machine ([ErrPeerSerialUnknown]). A serial the store does not
// have is accepted: TLS verified the certificate against the CA and the
// revocation list, and a hub restored from a backup older than the
// machine's last renewal has no row for its serial. RenewCert is stricter
// (checkPeer).
func checkConnectPeer(ctx context.Context, q querier, machineID, serial string) error {
	if err := checkMachine(ctx, q, machineID); err != nil {
		return err
	}
	var (
		owner   string
		revoked sql.NullInt64
	)
	err := q.QueryRowContext(ctx, `SELECT machine_id, revoked_time FROM cert_serials WHERE serial = ?`, serial).
		Scan(&owner, &revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("statedb: read peer serial: %w", err)
	case owner != machineID:
		return ErrPeerSerialUnknown
	case revoked.Valid:
		return ErrSerialRevoked
	}
	return nil
}

// displace closes the machine's live row, if any, as displaced by epoch.
func displace(ctx context.Context, tx *sql.Tx, in HelloIn, epoch uint64) (*Displaced, error) {
	var (
		d            Displaced
		old, started int64
	)
	err := tx.QueryRowContext(ctx, `SELECT epoch, boot_id, install_id, peer, started_time FROM machine_connections
		WHERE machine_id = ? AND ended_time IS NULL`, in.MachineID).Scan(&old, &d.BootID, &d.InstallID, &d.Peer, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("statedb: read live connection: %w", err)
	}
	d.Epoch, d.Started = uint64(old), time.UnixMilli(started) //nolint:gosec // G115: epoch has CHECK >= 1
	var age sql.NullInt64
	if l := in.Live; l.Epoch == d.Epoch && !l.LastHeartbeat.IsZero() { // d.Epoch >= 1, so Epoch 0 never matches
		d.HeartbeatAge = max(in.Now.Sub(l.LastHeartbeat), 0).Truncate(time.Millisecond)
		d.HeartbeatKnown = true
		age = sql.NullInt64{Int64: d.HeartbeatAge.Milliseconds(), Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE machine_connections SET ended_time = max(?, started_time), end_reason = ?, displaced_by = ?,
		last_heartbeat_age_ms = ? WHERE machine_id = ? AND epoch = ?`,
		in.Now.UnixMilli(), string(EndDisplaced), int64(epoch), age, in.MachineID, old); err != nil { //nolint:gosec // G115: epoch <= MaxEpoch
		return nil, fmt.Errorf("statedb: close displaced connection: %w", err)
	}
	return &d, nil
}

// EndConnection closes the machine's connection row of epoch with reason,
// as one writer job, if that row is still live, and then moves the
// machine's last_seen_time to now (defaults.md: written on disconnect). It
// reports whether it closed the live row: false for a row already closed or
// displaced, and for an epoch the machine never had, which changes nothing.
// Only a true result may change the machine's state (03f race table, "The
// displaced stream closes after the newer Hello"). EndDisplaced is Hello's
// alone ([ErrEndReasonInvalid]).
func EndConnection(ctx context.Context, s *store.Store, machineID string, epoch uint64, reason EndReason, now time.Time) (bool, error) {
	if !endReasons[reason] {
		return false, fmt.Errorf("%w: %q", ErrEndReasonInvalid, reason)
	}
	if epoch == 0 || epoch > MaxEpoch {
		return false, nil
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var closed bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(jctx, `UPDATE machine_connections SET ended_time = max(?, started_time), end_reason = ?
			WHERE machine_id = ? AND epoch = ? AND ended_time IS NULL`, now.UnixMilli(), string(reason), machineID, int64(epoch)) //nolint:gosec // G115: epoch <= MaxEpoch
		if err != nil {
			return fmt.Errorf("statedb: close connection: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("statedb: close connection: %w", err)
		}
		if n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(jctx, `UPDATE machines SET last_seen_time = max(ifnull(last_seen_time, 0), ?)
			WHERE machine_id = ?`, now.UnixMilli(), machineID); err != nil {
			return fmt.Errorf("statedb: store last seen: %w", err)
		}
		closed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return closed, nil
}
