package gateway

import (
	"context"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the gateway's post-commit hook into Watch (plan row 12,
// the Watch ingest slice): after an ingest, presence or lifecycle job
// commits, the arm calls its sink so Watch subscribers see the
// machine-driven change. Every call runs in the committing job's
// post-commit hook on the writer, in commit order (#393), and never
// publishes a rolled-back job. The zero WatchSink publishes nothing, so
// tests and tools that do not wire Watch keep the ingest path pure.

// WatchSink publishes committed gateway changes to Watch subscribers. The
// daemon wires each hook to a Fleet publish method; a nil hook skips its
// publish.
type WatchSink struct {
	// MachineState publishes a committed machines.state change (a
	// Snapshot job's ONLINE, housekeeping's STALE, a Heartbeat
	// promotion, a stream close's OFFLINE). from is the state the job
	// left, for UNMATCHED evaluation.
	MachineState func(ctx context.Context, machineID, from string)
	// MachineCreated publishes an enrolled machine with its create
	// write. Replays publish nothing.
	MachineCreated func(ctx context.Context, machineID string, replay bool)
	// MachineCommitted publishes a Snapshot job's machine write with
	// its before image. before is nil when the arm could not read one.
	MachineCommitted func(ctx context.Context, machineID string, before *statedb.MachineRow)
	// MachineEvent publishes an applied event job's machine stamp: the
	// core stamps only the seq, observed time and hub_seq, none of them
	// filterable, so the write is always volatile-only and waits out its
	// interval behind the Watch gate.
	MachineEvent func(ctx context.Context, machineID string)
	// GoalsCommitted publishes an ingest job's observed-goal writes: the
	// stamped after-images the job's appliers captured in its transaction,
	// diffed by stamp against the before image the arm read.
	GoalsCommitted func(ctx context.Context, machineID string, before, after []statedb.GoalRow)
	// Pane publishes one applied agent PaneSummary event with the
	// machine stamp of its event job.
	Pane func(ctx context.Context, p PanePublication)
}

// PanePublication is one agent PaneSummary event the hub publishes:
// the pane key and lines as reported, attributed to the stream's machine
// and the snapshot's session for the pane (empty for the developer's own
// panes), with the machine stamp Seq of the event job that carried it.
type PanePublication struct {
	Pane           *typev1.PaneKey
	MachineID      string
	SessionID      string
	LastLines      string
	LastOutputTime time.Time
	Activity       typev1.Activity
	Seq            int64
}

func (a *agentService) publishMachineState(ctx context.Context, machineID, from string) {
	if a.watch.MachineState != nil {
		a.watch.MachineState(ctx, machineID, from)
	}
}

func (a *agentService) publishMachineCreated(ctx context.Context, machineID string, replay bool) {
	if a.watch.MachineCreated != nil {
		a.watch.MachineCreated(ctx, machineID, replay)
	}
}

func (a *agentService) publishMachineCommitted(ctx context.Context, machineID string, before *statedb.MachineRow) {
	if a.watch.MachineCommitted != nil {
		a.watch.MachineCommitted(ctx, machineID, before)
	}
}

func (a *agentService) publishMachineEvent(ctx context.Context, machineID string) {
	if a.watch.MachineEvent != nil {
		a.watch.MachineEvent(ctx, machineID)
	}
}

func (a *agentService) publishGoalsCommitted(ctx context.Context, machineID string, before, after []statedb.GoalRow) {
	if a.watch.GoalsCommitted != nil {
		a.watch.GoalsCommitted(ctx, machineID, before, after)
	}
}

func (a *agentService) publishPane(ctx context.Context, p PanePublication) {
	if a.watch.Pane != nil {
		a.watch.Pane(ctx, p)
	}
}

// snapshotBefore reads the machine row and its goal rows a Snapshot job
// is about to replace, for the post-commit Watch publish. A machine read
// that fails degrades to a nil before (the publish carries no was); a
// goal read that fails reports ok false, and the arm skips the goal
// publish rather than mistaking every row for created.
func (a *agentService) snapshotBefore(ctx context.Context, machineID string) (*statedb.MachineRow, []statedb.GoalRow, bool) {
	var before *statedb.MachineRow
	if row, err := statedb.ReadMachineRow(ctx, a.store, machineID); err == nil {
		before = &row
	}
	goals, ok := a.machineGoals(ctx, machineID)
	return before, goals, ok
}

// machineGoals reads the machine's goal rows on the read pool: the
// before image a goal publish diffs against. ok is false when the read
// itself failed.
func (a *agentService) machineGoals(ctx context.Context, machineID string) ([]statedb.GoalRow, bool) {
	rows, err := statedb.ListGoalRows(ctx, a.store)
	if err != nil {
		return nil, false
	}
	var out []statedb.GoalRow
	for _, r := range rows {
		if r.MachineID == machineID {
			out = append(out, r)
		}
	}
	return out, true
}

// paneKeyString names a pane for the attribution map: the same form the
// Watch bus keys the summary by (watch.PaneSummary.Pane).
func paneKeyString(k *typev1.PaneKey) string {
	return k.GetServer() + "/" + k.GetServerStartId() + "/" + k.GetPaneId()
}

// panePublication attributes one PaneSummary event for the Watch
// publish: nil for every other kind. The session comes from the
// snapshot's pane map (empty for the developer's own panes, and when no
// snapshot named the pane yet); the stamp arrives at settle time from the
// event job.
func (a *agentService) panePublication(s *stream, ev *agentv1.Event) *PanePublication {
	ps := ev.GetPaneSummary()
	if ps == nil {
		return nil
	}
	var at time.Time
	if ts := ps.GetLastOutputTime(); ts != nil {
		at = ts.AsTime()
	}
	return &PanePublication{
		Pane: ps.GetPane(), MachineID: s.machineID,
		SessionID: s.panes[paneKeyString(ps.GetPane())],
		LastLines: ps.GetLastLines(), LastOutputTime: at,
		Activity: ps.GetActivity(),
	}
}

// noteSnapshotPanes remembers the snapshot's pane-to-session map for
// PaneSummary attribution: the events carry only the pane key, and the
// hub-assigned session id rides the snapshot's tmux inventory (empty for
// the developer's own panes). Only the stream's handler goroutine touches
// the map; a newer snapshot replaces it whole.
func (a *agentService) noteSnapshotPanes(s *stream, snap *agentv1.Snapshot) {
	panes := map[string]string{}
	for _, srv := range snap.GetTmuxServers() {
		for _, sess := range srv.GetSessions() {
			id := sess.GetSessionId().GetValue()
			for _, w := range sess.GetWindows() {
				for _, p := range w.GetPanes() {
					panes[paneKeyString(p.GetKey())] = id
				}
			}
		}
	}
	s.panes = panes
}
