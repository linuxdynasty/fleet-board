package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Applier is one later row's part of the Snapshot transaction (row 5
// ruling, fork 2a): it replaces that row's tables for machineID from snap,
// inside the writer job's tx, after the core (facts, seq, counters,
// policy) and before the machine is marked ONLINE. It must use only tx (no
// network call, no wait, no Commit) and treat snap as read-only cleaned
// data; an error rolls back the whole Snapshot, so the machine stays as it
// was and not ONLINE. Text a row stores beyond what [Clean] covers is
// cleaned before the job, never in Apply.
type Applier interface {
	Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) error
}

// ErrApplier wraps the error of a failing [Applier].
var ErrApplier = errors.New("ingest: a snapshot applier failed")

// Stream is the live Connect stream a Snapshot came on: the machine and
// epoch, and what its Hello said (02i rule 3 compares them with the store).
type Stream struct {
	MachineID    string
	Epoch        uint64
	LastEventSeq uint64
	Counters     statedb.EventCounters
	InstallID    string
}

// ApplySnapshot applies a cleaned snap ([Clean]) as ONE writer job:
// statedb.ApplySnapshot, then each applier in order, then
// statedb.MarkOnline. Its result arrives after the commit; any error rolls
// the whole job back. A ctx error means the outcome is unknown. now is the
// hub clock: observed_time, last_seen_time and an audit row's time.
func ApplySnapshot(ctx context.Context, st *store.Store, s Stream, snap *agentv1.Snapshot, appliers []Applier,
	now time.Time,
) (statedb.SnapshotOut, error) {
	in, err := snapshotIn(s, snap, now)
	if err != nil {
		return statedb.SnapshotOut{}, err
	}
	jctx := context.WithoutCancel(ctx) // the job runs on the writer; ctx only bounds the wait
	var out statedb.SnapshotOut
	err = st.Write(ctx, func(tx *sql.Tx) error {
		var err error
		if out, err = statedb.ApplySnapshot(jctx, tx, in); err != nil {
			return err
		}
		for i, a := range appliers {
			if err := a.Apply(jctx, tx, s.MachineID, snap); err != nil {
				return fmt.Errorf("%w: applier %d: %w", ErrApplier, i, err)
			}
		}
		_, err = statedb.MarkOnline(jctx, tx, s.MachineID, now)
		return err
	})
	if err != nil {
		return statedb.SnapshotOut{}, err
	}
	return out, nil
}

// snapshotIn is the statedb input: the facts and resources as protojson
// (the Enroll encoding of machines.facts), the policy and the drain.
func snapshotIn(s Stream, snap *agentv1.Snapshot, now time.Time) (statedb.SnapshotIn, error) {
	facts := snap.GetFacts()
	if facts == nil {
		facts = &agentv1.MachineFacts{}
	}
	fj, err := protojson.Marshal(facts)
	if err != nil {
		return statedb.SnapshotIn{}, fmt.Errorf("ingest: encode facts: %w", err)
	}
	var resources string
	if r := snap.GetResources(); r != nil {
		rj, err := protojson.Marshal(r)
		if err != nil {
			return statedb.SnapshotIn{}, fmt.Errorf("ingest: encode resources: %w", err)
		}
		resources = string(rj)
	}
	p := snap.GetPolicy()
	var loaded time.Time
	if p.GetLoadedTime() != nil {
		loaded = p.GetLoadedTime().AsTime()
	}
	d := snap.GetDrain()
	return statedb.SnapshotIn{
		MachineID: s.MachineID, Epoch: s.Epoch, Seq: snap.GetSeq(), Facts: string(fj), Resources: resources,
		Policy: statedb.Policy{
			Hash: p.GetHash(), SchemaVersion: p.GetSchemaVersion(), Error: p.GetError(), Loaded: loaded,
			ChangedSinceLoad: p.GetChangedSinceLoad(), SafeMode: p.GetSafeMode(),
		},
		Drain: statedb.Drain{
			Draining: d.GetDraining(),
			Local:    d.GetRequestedByPrincipal().GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		},
		LastEventSeq: s.LastEventSeq, Counters: s.Counters, InstallID: s.InstallID, Now: now,
	}, nil
}
