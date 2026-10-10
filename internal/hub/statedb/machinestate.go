package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// The other stored machine states (02g "Machine lifecycle"; MachineEnrolled
// is in machines.go). Plan row 5c's Snapshot transaction marks ONLINE; the
// changes here move a machine between ONLINE, STALE and OFFLINE.
const (
	MachineOnline  = "ONLINE"
	MachineStale   = "STALE"
	MachineOffline = "OFFLINE"
	MachineRevoked = "REVOKED"
)

// The reasons a [StateChange] carries besides the end reasons of a live
// row's close (stream_closed, agent_bye, ingest_stalled, offline).
const (
	ReasonSilence   = "silence"   // housekeeping: no Heartbeat for 45 s (STALE) or 3 min (OFFLINE)
	ReasonHeartbeat = "heartbeat" // a Heartbeat on the live stream brought a STALE machine back
	ReasonHubStart  = "hub_start" // the start sweep: ONLINE becomes STALE
)

// StateChange is one committed change of machines.state, stamped for Watch
// (status observed_time and hub_seq). No audit row is written: 02m lists no
// state-change action.
type StateChange struct {
	MachineID, From, To, Reason string
	Stamp                       Stamp
}

// Ended is what [EndConnection] did: Live is true when it closed the
// machine's live row, and Change is the state change that close made, if
// any.
type Ended struct {
	Live   bool
	Change *StateChange
}

// offlineOnClose are the end reasons whose live-row close takes an ONLINE or
// STALE machine OFFLINE at once (03n, 02g). Not hub_restart: the hub's own
// shutdown is not the machine leaving, and the start sweep turns ONLINE into
// STALE. Not revoked: RevokeMachine (13a) writes REVOKED itself.
var offlineOnClose = map[EndReason]bool{
	EndStreamClosed: true, EndAgentBye: true, EndIngestStalled: true, EndOffline: true,
}

// EndConnection closes the machine's connection row of epoch with reason,
// as one writer job, if that row is still live, and then moves the
// machine's last_seen_time to now (defaults.md: written on disconnect). In
// the same job a close of the live row by the stream, the agent's Bye or a
// housekeeping cut takes an ONLINE or STALE machine OFFLINE (Ended.Change).
// A row already closed or displaced, or an epoch the machine never had,
// changes nothing, so a displaced stream never marks its machine OFFLINE
// (03f race table, "The displaced stream closes after the newer Hello";
// #138). EndDisplaced is Hello's alone ([ErrEndReasonInvalid]).
func EndConnection(ctx context.Context, s *store.Store, machineID string, epoch uint64, reason EndReason, now time.Time) (Ended, error) {
	if !endReasons[reason] {
		return Ended{}, fmt.Errorf("%w: %q", ErrEndReasonInvalid, reason)
	}
	if epoch == 0 || epoch > MaxEpoch {
		return Ended{}, nil
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out Ended
	err := s.Write(ctx, func(tx *sql.Tx) error {
		out = Ended{}
		live, err := closeLive(jctx, tx, machineID, epoch, reason, now)
		if err != nil || !live {
			return err
		}
		out.Live = true
		if !offlineOnClose[reason] {
			return nil
		}
		out.Change, err = changeState(jctx, tx, machineID, []string{MachineOnline, MachineStale}, MachineOffline, string(reason), now)
		return err
	})
	if err != nil {
		return Ended{}, err
	}
	return out, nil
}

// closeLive closes machineID's row of epoch with reason if it is live and
// moves last_seen_time; live reports whether it did.
func closeLive(ctx context.Context, tx *sql.Tx, machineID string, epoch uint64, reason EndReason, now time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE machine_connections SET ended_time = max(?, started_time), end_reason = ?
		WHERE machine_id = ? AND epoch = ? AND ended_time IS NULL`, now.UnixMilli(), string(reason), machineID, int64(epoch)) //nolint:gosec // G115: epoch <= MaxEpoch
	if err != nil {
		return false, fmt.Errorf("statedb: close connection: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("statedb: close connection: %w", err)
	}
	if n == 0 {
		return false, nil
	}
	return true, touchSeen(ctx, tx, machineID, now)
}

// touchSeen moves last_seen_time forward to now; it never steps back.
func touchSeen(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE machines SET last_seen_time = max(ifnull(last_seen_time, 0), ?)
		WHERE machine_id = ?`, now.UnixMilli(), machineID); err != nil {
		return fmt.Errorf("statedb: store last seen: %w", err)
	}
	return nil
}

// changeState moves machineID to `to` when its state is one of from,
// stamping the row; nil when it was not (or there is no row).
func changeState(ctx context.Context, tx *sql.Tx, machineID string, from []string, to, reason string, now time.Time) (*StateChange, error) {
	var cur string
	err := tx.QueryRowContext(ctx, `SELECT state FROM machines WHERE machine_id = ?`, machineID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("statedb: read machine state: %w", err)
	}
	if !slices.Contains(from, cur) {
		return nil, nil
	}
	st, err := NextStamp(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE machines SET state = ?, observed_time = ?, hub_seq = ? WHERE machine_id = ?`,
		to, st.ObservedTime.UnixMilli(), st.HubSeq, machineID); err != nil {
		return nil, fmt.Errorf("statedb: store machine state: %w", err)
	}
	return &StateChange{MachineID: machineID, From: cur, To: to, Reason: reason, Stamp: st}, nil
}

// liveEpoch is machineID's live connection epoch, 0 when it has none.
func liveEpoch(ctx context.Context, tx *sql.Tx, machineID string) (uint64, error) {
	var e int64
	err := tx.QueryRowContext(ctx, `SELECT epoch FROM machine_connections WHERE machine_id = ? AND ended_time IS NULL`,
		machineID).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("statedb: read live connection: %w", err)
	}
	return uint64(e), nil //nolint:gosec // G115: epoch has CHECK >= 1
}

// Mark is one housekeeping decision: To (STALE or OFFLINE) for MachineID,
// judged while Epoch was its live connection (0 for none).
type Mark struct {
	MachineID string
	Epoch     uint64
	To        string
	Now       time.Time
}

// MarkState applies a housekeeping decision (reason "silence") as one
// writer job, only while it still holds: the machine's live epoch is still
// m.Epoch (a Hello that committed after the read wins) and its state is
// ONLINE (for STALE) or ONLINE or STALE (for OFFLINE). OFFLINE also closes
// the live row as offline in the same job, so nothing is left live for a
// stream that is about to be cut or that no stream holds. It returns the
// change, or nil when the decision no longer held.
func MarkState(ctx context.Context, s *store.Store, m Mark) (*StateChange, error) {
	from := []string{MachineOnline}
	switch m.To {
	case MachineStale:
	case MachineOffline:
		from = append(from, MachineStale)
	default:
		return nil, fmt.Errorf("statedb: housekeeping cannot mark %q", m.To)
	}
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out *StateChange
	err := s.Write(ctx, func(tx *sql.Tx) error {
		out = nil
		live, err := liveEpoch(jctx, tx, m.MachineID)
		if err != nil || live != m.Epoch {
			return err
		}
		if out, err = changeState(jctx, tx, m.MachineID, from, m.To, ReasonSilence, m.Now); err != nil || out == nil {
			return err
		}
		if m.To == MachineOffline && live != 0 {
			_, err = closeLive(jctx, tx, m.MachineID, live, EndOffline, m.Now)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Beat is what a Heartbeat on a live stream asks the store for: last_seen
// at Now (at most once a minute, the caller's pacing), and with Promote a
// STALE machine back to ONLINE while Epoch is its live connection.
type Beat struct {
	MachineID string
	Epoch     uint64
	Promote   bool
	Now       time.Time
}

// HeartbeatSeen moves last_seen_time and, with b.Promote, returns a STALE
// machine to ONLINE (reason "heartbeat") when b.Epoch is still its live
// connection, in one writer job. It returns the change, or nil.
func HeartbeatSeen(ctx context.Context, s *store.Store, b Beat) (*StateChange, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out *StateChange
	err := s.Write(ctx, func(tx *sql.Tx) error {
		out = nil
		if err := touchSeen(jctx, tx, b.MachineID, b.Now); err != nil || !b.Promote {
			return err
		}
		live, err := liveEpoch(jctx, tx, b.MachineID)
		if err != nil || live == 0 || live != b.Epoch {
			return err
		}
		out, err = changeState(jctx, tx, b.MachineID, []string{MachineStale}, MachineOnline, ReasonHeartbeat, b.Now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
