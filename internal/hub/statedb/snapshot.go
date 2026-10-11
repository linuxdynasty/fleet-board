package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// snapshotSQL is migration 8 (plan row 5, PR 5c1): what the Snapshot
// transaction keeps on the machine row (row 5 ruling, fork 2a; no
// inventory tables, rows 7 to 10 add theirs).
//
//   - hub_last_event_seq: the highest event seq stored (5c2 advances it;
//     HelloAck sends it); event_generation: 1, then one more per agent
//     rollback (02i rule 3); the Hello's EventCounters.
//   - snapshot_seq and snapshot_epoch: the last applied Snapshot.seq (the
//     floor at or below which a replayed event is history only) and the
//     epoch it came on.
//   - resources: ResourceSample as protojson, NULL when unknown.
//   - policy_* (03e "Persistence") and the drain the agent reports.
const snapshotSQL = `ALTER TABLE machines ADD COLUMN hub_last_event_seq INTEGER NOT NULL DEFAULT 0
	CHECK (hub_last_event_seq >= 0);
ALTER TABLE machines ADD COLUMN event_generation INTEGER NOT NULL DEFAULT 1 CHECK (event_generation >= 1);
ALTER TABLE machines ADD COLUMN snapshot_seq INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_seq >= 0);
ALTER TABLE machines ADD COLUMN snapshot_epoch INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_epoch >= 0);
ALTER TABLE machines ADD COLUMN events_shed_total INTEGER NOT NULL DEFAULT 0 CHECK (events_shed_total >= 0);
ALTER TABLE machines ADD COLUMN events_coalesced_total INTEGER NOT NULL DEFAULT 0 CHECK (events_coalesced_total >= 0);
ALTER TABLE machines ADD COLUMN events_evicted_total INTEGER NOT NULL DEFAULT 0 CHECK (events_evicted_total >= 0);
ALTER TABLE machines ADD COLUMN resources TEXT;
ALTER TABLE machines ADD COLUMN policy_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN policy_schema_version TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN policy_loaded_time INTEGER;
ALTER TABLE machines ADD COLUMN policy_changed_since_load INTEGER NOT NULL DEFAULT 0
	CHECK (policy_changed_since_load IN (0, 1));
ALTER TABLE machines ADD COLUMN policy_safe_mode INTEGER NOT NULL DEFAULT 0 CHECK (policy_safe_mode IN (0, 1));
ALTER TABLE machines ADD COLUMN policy_error TEXT NOT NULL DEFAULT '';
ALTER TABLE machines ADD COLUMN agent_draining INTEGER NOT NULL DEFAULT 0 CHECK (agent_draining IN (0, 1));
ALTER TABLE machines ADD COLUMN agent_drain_local INTEGER NOT NULL DEFAULT 0 CHECK (agent_drain_local IN (0, 1));`

// ActionInstallChanged is the audit action of an agent rollback (02i rule
// 3, 02g, 02m).
const ActionInstallChanged = "machine.install_changed"

var (
	// ErrSnapshotNotCurrent: the Snapshot's epoch is not the machine's live
	// connection (a newer Hello displaced it, or its row closed).
	ErrSnapshotNotCurrent = errors.New("statedb: the snapshot is not on the machine's current connection")
	// ErrSnapshotInvalid: a number above 2^63 - 1, or no hub time.
	ErrSnapshotInvalid = errors.New("statedb: the snapshot is malformed")
)

// EventCounters are Hello.event_counters (02i "Gaps").
type EventCounters struct {
	Shed, Coalesced, Evicted uint64
}

// Policy is Snapshot.policy (PolicyInfo), its text already cleaned.
type Policy struct {
	Hash, SchemaVersion, Error string
	Loaded                     time.Time // zero when unset
	ChangedSinceLoad, SafeMode bool
}

// Drain is what Snapshot.drain says: draining, and whether a local
// `fleet-agent drain` asked for it (the hub cannot undo that).
type Drain struct {
	Draining, Local bool
}

// SnapshotIn is one Snapshot the gateway took on a live stream, its text
// already capped and scrubbed (never in the transaction), with the values
// that stream's Hello carried.
type SnapshotIn struct {
	MachineID string
	// Epoch is the stream's connection epoch; Seq is Snapshot.seq.
	Epoch, Seq uint64
	// Facts is MachineFacts as protojson; Resources ResourceSample as
	// protojson, empty when the snapshot had none (stored as NULL).
	Facts, Resources string
	Policy           Policy
	Drain            Drain
	// LastEventSeq, Counters and InstallID are the stream's Hello values.
	LastEventSeq uint64
	Counters     EventCounters
	InstallID    string
	// Now is the hub clock the caller read when it built the job.
	Now time.Time
}

// SnapshotOut is what [ApplySnapshot] decided.
type SnapshotOut struct {
	// Generation is the machine's event_generation after the Snapshot.
	Generation uint64
	// Rollback names the 02i rule 3 causes found on the epoch's first
	// Snapshot, in a fixed order (last_event_seq, counters, install_id,
	// snapshot_seq); nil when none, and always nil for a later Snapshot on
	// the same epoch. snapshot_seq also asks for SnapshotRequest{seq_rollback}.
	Rollback []string
	// Gap is set when the epoch's first Snapshot came with a Hello whose
	// evicted_total is above the stored one and no rollback: events were
	// lost to ring overflow while the agent was offline (02i "Gaps"). A gap
	// marker row was written with it.
	Gap *Gap
	// Change is the state change [MarkOnline] made at the end of the job
	// (the job ingest.SubmitSnapshot queues fills it); nil when the machine was ONLINE.
	Change *StateChange
}

// Gap is ring overflow while the agent was offline: Evicted more events
// lost, after FromSeq (the hub_last_event_seq the Snapshot found).
type Gap struct {
	FromSeq, Evicted uint64
}

// ApplySnapshot is the core of the Snapshot transaction, in the writer
// job's tx: the machine must exist, not be REVOKED, and hold a live
// connection row of in.Epoch that is its last epoch
// ([ErrSnapshotNotCurrent]); then, on the epoch's first Snapshot only, it
// detects an agent rollback, which bumps event_generation and appends
// machine.install_changed; it replaces the facts, resources, policy, drain
// and the seq floor, and (first Snapshot only) the counters and install_id.
// It leaves machines.state alone: [MarkOnline] ends the job, after the
// appliers of later rows. Errors leave the job to roll back.
func ApplySnapshot(ctx context.Context, tx *sql.Tx, in SnapshotIn) (SnapshotOut, error) {
	if err := in.validate(); err != nil {
		return SnapshotOut{}, err
	}
	var (
		state, install                     string
		lastEpoch, snapEpoch, hubLast, gen int64
		shed, coalesced, evicted, ok       int64
	)
	err := tx.QueryRowContext(ctx, `SELECT state, install_id, last_epoch, snapshot_epoch, hub_last_event_seq,
		event_generation, events_shed_total, events_coalesced_total, events_evicted_total,
		EXISTS (SELECT 1 FROM machine_connections c WHERE c.machine_id = m.machine_id AND c.epoch = ? AND c.ended_time IS NULL)
		FROM machines m WHERE machine_id = ?`, int64(in.Epoch), in.MachineID). //nolint:gosec // G115: validated
		Scan(&state, &install, &lastEpoch, &snapEpoch, &hubLast, &gen, &shed, &coalesced, &evicted, &ok)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return SnapshotOut{}, ErrMachineUnknown
	case err != nil:
		return SnapshotOut{}, fmt.Errorf("statedb: read machine for snapshot: %w", err)
	case state == "REVOKED":
		return SnapshotOut{}, ErrMachineRevoked
	case ok == 0 || uint64(lastEpoch) != in.Epoch: //nolint:gosec // G115: CHECK >= 0
		return SnapshotOut{}, fmt.Errorf("%w: epoch %d, machine at %d", ErrSnapshotNotCurrent, in.Epoch, lastEpoch)
	}
	out := SnapshotOut{Generation: uint64(gen)} //nolint:gosec // G115: CHECK >= 1
	// Only the epoch's first Snapshot judges the stream's Hello and its seq
	// (02i rule 3). Later Snapshots on the epoch (SnapshotRequest answers)
	// carry the same Hello: judged again they would start a generation, an
	// audit row and a SnapshotRequest per answer, and write the Hello's
	// counters over newer ones. They keep the stored counters and install_id.
	c, installID := in.Counters, in.InstallID
	if snapEpoch == lastEpoch {
		c = EventCounters{Shed: uint64(shed), Coalesced: uint64(coalesced), Evicted: uint64(evicted)} //nolint:gosec // G115: CHECK >= 0
		installID = install
	} else {
		for _, r := range []struct {
			cause string
			hit   bool
		}{
			{"last_event_seq", int64(in.LastEventSeq) < hubLast},                                               //nolint:gosec // G115: validated
			{"counters", int64(c.Shed) < shed || int64(c.Coalesced) < coalesced || int64(c.Evicted) < evicted}, //nolint:gosec // G115: validated
			{"install_id", installID != install},
			{"snapshot_seq", hubLast > 0 && int64(in.Seq) <= hubLast}, //nolint:gosec // G115: validated
		} {
			if r.hit {
				out.Rollback = append(out.Rollback, r.cause)
			}
		}
	}
	if out.Rollback == nil && snapEpoch != lastEpoch && int64(c.Evicted) > evicted { //nolint:gosec // G115: validated
		out.Gap = &Gap{FromSeq: uint64(hubLast), Evicted: c.Evicted - uint64(evicted)} //nolint:gosec // G115: CHECK >= 0
	}
	if out.Rollback != nil {
		out.Generation++
		if err := auditRollback(ctx, tx, in, install, out); err != nil {
			return SnapshotOut{}, err
		}
	}
	var loaded, resources any
	if !in.Policy.Loaded.IsZero() {
		loaded = in.Policy.Loaded.UnixMilli()
	}
	if in.Resources != "" {
		resources = in.Resources
	}
	p, d := in.Policy, in.Drain
	_, err = tx.ExecContext(ctx, `UPDATE machines SET facts = ?, resources = ?, policy_hash = ?, policy_schema_version = ?,
		policy_loaded_time = ?, policy_changed_since_load = ?, policy_safe_mode = ?, policy_error = ?, agent_draining = ?,
		agent_drain_local = ?, events_shed_total = ?, events_coalesced_total = ?, events_evicted_total = ?, install_id = ?,
		event_generation = ?, snapshot_seq = ?, snapshot_epoch = ?, last_seen_time = max(ifnull(last_seen_time, 0), ?)
		WHERE machine_id = ?`,
		in.Facts, resources, p.Hash, p.SchemaVersion, loaded, p.ChangedSinceLoad, p.SafeMode, p.Error, d.Draining, d.Local,
		int64(c.Shed), int64(c.Coalesced), int64(c.Evicted), installID, int64(out.Generation), //nolint:gosec // G115: validated
		int64(in.Seq), int64(in.Epoch), in.Now.UnixMilli(), in.MachineID) //nolint:gosec // G115: validated
	if err != nil {
		return SnapshotOut{}, fmt.Errorf("statedb: store snapshot: %w", err)
	}
	if err := snapshotMarkers(ctx, tx, in, out); err != nil {
		return SnapshotOut{}, err
	}
	return out, nil
}

// snapshotMarkers puts the timeline marks of the epoch's first Snapshot
// in the event history, under the generation just stored: a state reset
// for a rollback (02i rule 3), a gap for ring overflow (02i "Gaps").
func snapshotMarkers(ctx context.Context, tx *sql.Tx, in SnapshotIn, out SnapshotOut) error {
	var m Marker
	switch {
	case out.Rollback != nil:
		d, err := auditDetail(struct {
			Causes     []string `json:"causes"`
			Generation uint64   `json:"event_generation"`
		}{out.Rollback, out.Generation})
		if err != nil {
			return err
		}
		m = Marker{Kind: MarkerStateReset, Detail: d}
	case out.Gap != nil:
		d, err := auditDetail(struct {
			FromSeq uint64 `json:"from_seq"`
			Evicted uint64 `json:"evicted"`
		}{out.Gap.FromSeq, out.Gap.Evicted})
		if err != nil {
			return err
		}
		m = Marker{Kind: MarkerGap, Detail: d}
	default:
		return nil
	}
	m.MachineID, m.Now = in.MachineID, in.Now
	return AppendMarker(ctx, tx, m)
}

func (in SnapshotIn) validate() error {
	for _, v := range []uint64{in.Epoch, in.Seq, in.LastEventSeq, in.Counters.Shed, in.Counters.Coalesced, in.Counters.Evicted} {
		if v > math.MaxInt64 {
			return fmt.Errorf("%w: a number above 2^63 - 1", ErrSnapshotInvalid)
		}
	}
	if in.Now.IsZero() || in.MachineID == "" || in.InstallID == "" {
		return fmt.Errorf("%w: it needs the machine, its install_id and the hub's time", ErrSnapshotInvalid)
	}
	return nil
}

// auditRollback appends the machine.install_changed row of an agent
// rollback: no actor, the causes, both generations and both install ids.
func auditRollback(ctx context.Context, tx *sql.Tx, in SnapshotIn, oldInstall string, out SnapshotOut) error {
	detail, err := auditDetail(struct {
		Causes        []string `json:"causes"`
		OldGeneration uint64   `json:"old_generation"`
		NewGeneration uint64   `json:"new_generation"`
		OldInstallID  string   `json:"old_install_id"`
		NewInstallID  string   `json:"new_install_id"`
	}{out.Rollback, out.Generation - 1, out.Generation, oldInstall, in.InstallID})
	if err != nil {
		return err
	}
	_, err = AppendAudit(ctx, tx, AuditEntry{
		Action: ActionInstallChanged, Target: "machines/" + in.MachineID, MachineID: in.MachineID, Detail: detail, Now: in.Now,
	})
	return err
}

// MarkOnline ends the Snapshot transaction: the machine is ONLINE and its
// status is stamped (one hub_seq, observed_time = now). It runs after
// [ApplySnapshot] and every applier in the same job, so a failure of any
// of them leaves the machine as it was (row 5 ruling, fork 1). The change
// it made (reason [ReasonSnapshot]) is nil when the machine was already
// ONLINE (a second Snapshot on the stream).
func MarkOnline(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) (Stamp, *StateChange, error) {
	var from string
	err := tx.QueryRowContext(ctx, `SELECT state FROM machines WHERE machine_id = ? AND state <> 'REVOKED'`, machineID).Scan(&from)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Stamp{}, nil, ErrMachineUnknown
	case err != nil:
		return Stamp{}, nil, fmt.Errorf("statedb: mark online: %w", err)
	}
	st, err := NextStamp(ctx, tx, now)
	if err != nil {
		return Stamp{}, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE machines SET state = 'ONLINE', observed_time = ?, hub_seq = ?
		WHERE machine_id = ?`, st.ObservedTime.UnixMilli(), st.HubSeq, machineID); err != nil {
		return Stamp{}, nil, fmt.Errorf("statedb: mark online: %w", err)
	}
	if from == MachineOnline {
		return st, nil, nil
	}
	return st, &StateChange{MachineID: machineID, From: from, To: MachineOnline, Reason: ReasonSnapshot, Stamp: st}, nil
}
