package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// MachineSnapshot is what the Hello and Snapshot transactions keep on a
// machine row (migration 8), as [ReadSnapshot] reads it.
type MachineSnapshot struct {
	State string
	// Facts is MachineFacts as protojson; Resources ResourceSample as
	// protojson, empty when unknown.
	Facts, Resources string
	Policy           Policy
	Drain            Drain
	Counters         EventCounters
	InstallID        string
	// SnapshotSeq and SnapshotEpoch are the last applied Snapshot's; 0 when
	// none was applied.
	HubLastEventSeq, EventGeneration, SnapshotSeq, SnapshotEpoch uint64
}

// ReadSnapshot reads machineID's stored Snapshot state on the read pool
// ([ErrMachineUnknown] when there is no row).
func ReadSnapshot(ctx context.Context, s *store.Store, machineID string) (MachineSnapshot, error) {
	var (
		m                        MachineSnapshot
		resources                sql.NullString
		loaded                   sql.NullInt64
		hubLast, gen, seq, epoch int64
		shed, coalesced, evicted int64
	)
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT state, facts, resources, policy_hash, policy_schema_version, policy_error,
			policy_loaded_time, policy_changed_since_load, policy_safe_mode, agent_draining, agent_drain_local,
			events_shed_total, events_coalesced_total, events_evicted_total, install_id, hub_last_event_seq,
			event_generation, snapshot_seq, snapshot_epoch FROM machines WHERE machine_id = ?`, machineID).
			Scan(&m.State, &m.Facts, &resources, &m.Policy.Hash, &m.Policy.SchemaVersion, &m.Policy.Error, &loaded,
				&m.Policy.ChangedSinceLoad, &m.Policy.SafeMode, &m.Drain.Draining, &m.Drain.Local, &shed, &coalesced,
				&evicted, &m.InstallID, &hubLast, &gen, &seq, &epoch)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return MachineSnapshot{}, ErrMachineUnknown
	}
	if err != nil {
		return MachineSnapshot{}, fmt.Errorf("statedb: read snapshot state: %w", err)
	}
	m.Resources = resources.String
	if loaded.Valid {
		m.Policy.Loaded = time.UnixMilli(loaded.Int64)
	}
	//nolint:gosec // G115: every column has CHECK >= 0
	m.Counters = EventCounters{Shed: uint64(shed), Coalesced: uint64(coalesced), Evicted: uint64(evicted)}
	//nolint:gosec // G115: every column has CHECK >= 0
	m.HubLastEventSeq, m.EventGeneration, m.SnapshotSeq, m.SnapshotEpoch = uint64(hubLast), uint64(gen), uint64(seq), uint64(epoch)
	return m, nil
}
