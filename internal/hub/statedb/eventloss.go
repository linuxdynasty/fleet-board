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

// EventLoss is a Heartbeat whose EventCounters.evicted_total rose on the
// live stream: a NEVER_DROP or COALESCE event was lost while connected
// (02i "Gaps").
type EventLoss struct {
	MachineID string
	Epoch     uint64
	// Counters are the Heartbeat's; Evicted is above the stored value.
	Counters EventCounters
	Now      time.Time
}

// RecordEventLoss is one writer job: while in.Epoch is the machine's live
// connection, it stores the Heartbeat's counters (each only upward, so the
// next Hello is not judged a second gap for the same loss) and appends a
// gap marker after hub_last_event_seq. It returns the stored
// hub_last_event_seq; a stream that is no longer live is
// [ErrEventNotCurrent] and writes nothing.
func RecordEventLoss(ctx context.Context, st *store.Store, in EventLoss) (uint64, error) {
	c := in.Counters
	if c.Shed > math.MaxInt64 || c.Coalesced > math.MaxInt64 || c.Evicted > math.MaxInt64 || in.Epoch > math.MaxInt64 {
		return 0, fmt.Errorf("%w: a counter above 2^63 - 1", ErrEventInvalid)
	}
	var from uint64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		var hubLast, evicted int64
		err := tx.QueryRowContext(ctx, `SELECT hub_last_event_seq, events_evicted_total FROM machines m
			WHERE machine_id = ? AND state <> 'REVOKED' AND last_epoch = ? AND EXISTS (SELECT 1 FROM machine_connections c
			WHERE c.machine_id = m.machine_id AND c.epoch = m.last_epoch AND c.ended_time IS NULL)`,
			in.MachineID, int64(in.Epoch)).Scan(&hubLast, &evicted) //nolint:gosec // G115: checked above
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return ErrEventNotCurrent
		case err != nil:
			return fmt.Errorf("statedb: read machine for event loss: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET events_shed_total = max(events_shed_total, ?),
			events_coalesced_total = max(events_coalesced_total, ?), events_evicted_total = max(events_evicted_total, ?)
			WHERE machine_id = ?`, int64(c.Shed), int64(c.Coalesced), int64(c.Evicted), in.MachineID); err != nil { //nolint:gosec // G115: checked above
			return fmt.Errorf("statedb: store event counters: %w", err)
		}
		from = uint64(hubLast) //nolint:gosec // G115: CHECK >= 0
		d, err := auditDetail(struct {
			FromSeq uint64 `json:"from_seq"`
			Evicted int64  `json:"evicted"`
		}{from, max(int64(c.Evicted)-evicted, 0)}) //nolint:gosec // G115: checked above
		if err != nil {
			return err
		}
		return AppendMarker(ctx, tx, Marker{MachineID: in.MachineID, Kind: MarkerGap, Detail: d, Now: in.Now})
	})
	return from, err
}

// PruneEvents deletes, in one writer job, at most limit event rows (and
// markers) whose hub observed_time is before cutoff, oldest first (03f
// `events`: 1 week). It returns how many it deleted; the caller runs it
// again while that equals limit, so no one job holds the writer long.
func PruneEvents(ctx context.Context, st *store.Store, cutoff time.Time, limit int) (int64, error) {
	if limit <= 0 || cutoff.IsZero() {
		return 0, fmt.Errorf("%w: prune needs a cutoff and a positive limit", ErrEventInvalid)
	}
	var n int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE id IN
			(SELECT id FROM events WHERE observed_time < ? ORDER BY observed_time LIMIT ?)`, cutoff.UnixMilli(), limit)
		if err != nil {
			return fmt.Errorf("statedb: prune events: %w", err)
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}
