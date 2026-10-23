package api

import (
	"context"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file publishes machine-driven (ingest) commits to Watch streams
// (02c): the snapshot and event jobs (goals, machines, pane summaries),
// the syncer pages (work items, change requests) and hub-started sessions.
// Every method runs in the committing job's post-commit hook on the
// writer, in commit order (#393), and takes watchMu briefly: a rolled-back
// job publishes nothing, and concurrent publishers serialize in commit
// order.
//
// reads re-read the row List serves, so live filtering can never disagree
// with the initial state; offerWatch drops a non-positive stamp, so rows
// from before their stamp migration never publish as live.

// PublishMachineState publishes a committed machines.state change (02c):
// ONLINE from a Snapshot job, STALE or OFFLINE from housekeeping, OFFLINE
// from a stream close, ONLINE from a Heartbeat promotion. row is the
// after-image the job captured in its tx; from is the state the job left,
// for UNMATCHED evaluation. A state change is never volatile: it publishes
// at once. The caller holds no lock.
func (f *Fleet) PublishMachineState(ctx context.Context, row statedb.MachineRow, from string) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	was := row
	was.State = from
	f.volatile.last[gateKey(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, row.MachineID)] = f.now()
	f.offerWatchLocked(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, row) },
		was:   func(n *filterNode) bool { return nodeMatchesMachine(n, was) },
	})
}

// PublishMachineCreated publishes an enrolled machine with its create
// write. row is the after-image the enroll job captured in its tx.
// Replays publish nothing: the first enroll already did.
func (f *Fleet) PublishMachineCreated(ctx context.Context, row statedb.MachineRow, replay bool) {
	if replay {
		return
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	f.volatile.last[gateKey(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, row.MachineID)] = f.now()
	f.offerWatchLocked(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, row) },
	})
}

// PublishMachineCommitted publishes a Snapshot job's machine write with
// its before image for UNMATCHED evaluation. after is the after-image the
// job captured in its tx; before is nil when the caller did not read one
// (or the machine is new to the hub); volatile-only writes wait out their
// interval.
func (f *Fleet) PublishMachineCommitted(ctx context.Context, after statedb.MachineRow, before *statedb.MachineRow) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	volatileOnly := before != nil && machineVolatileOnly(*before, after)
	if !f.volatileAllow(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, after.MachineID, volatileOnly, machineVolatileInterval) {
		return
	}
	m, err := machineProto(after)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	var was func(*filterNode) bool
	if before != nil {
		old := *before
		was = func(n *filterNode) bool { return nodeMatchesMachine(n, old) }
	}
	f.offerWatchLocked(after.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, after) },
		was:   was,
	})
}

// PublishMachineEvent publishes an applied event job's machine stamp: the
// core stamps only hub_last_event_seq, observed_time and hub_seq, none of
// them filterable, so the write is always volatile-only and waits out its
// interval behind the gate. after is the after-image the job captured in
// its tx. The caller holds no lock.
func (f *Fleet) PublishMachineEvent(ctx context.Context, after statedb.MachineRow) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	if !f.volatileAllow(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, after.MachineID, true, machineVolatileInterval) {
		return
	}
	m, err := machineProto(after)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	f.offerWatchLocked(after.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, after) },
	})
}

// PublishGoalsCommitted publishes an ingest job's observed-goal writes:
// the snapshot or event applier's upserts and ends, diffed by stamp
// against the caller's before image. before holds the machine's goal rows
// as the commit found them; after holds the stamped after-images the job's
// appliers captured in its transaction, so rapid transitions publish every
// intermediate change instead of re-reading the latest state twice. A
// goal whose view does not parse logs publish_failed and skips only that
// goal. The caller holds no lock.
func (f *Fleet) PublishGoalsCommitted(ctx context.Context, machineID string, before, after []statedb.GoalRow) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	old := map[string]statedb.GoalRow{}
	for _, b := range before {
		old[b.ID] = b
	}
	type offer struct {
		seq    int64
		id     string
		change watchChange
	}
	var offers []offer
	// Publish the job's own after-images, never the current rows: later
	// jobs may already have committed newer states, and re-reading would
	// publish the newest state once per settlement, losing every
	// intermediate change.
	for _, row := range after {
		if row.MachineID != machineID {
			continue
		}
		b, ok := old[row.ID]
		if ok && b.HubSeq == row.HubSeq {
			continue
		}
		v, err := goalViewOf(row)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
			continue
		}
		g, err := goalProto(v)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
			continue
		}
		change := hubv1.ChangeType_CHANGE_TYPE_UPDATED
		if !ok {
			change = hubv1.ChangeType_CHANGE_TYPE_CREATED
		}
		var was func(*filterNode) bool
		volatileOnly := false
		if ok {
			if bv, err := goalViewOf(b); err == nil {
				was = func(n *filterNode) bool { return nodeMatchesGoal(n, bv) }
				volatileOnly = goalVolatileOnly(bv, v)
			}
		}
		if !f.volatileAllow(hubv1.WatchTopic_WATCH_TOPIC_GOALS, row.ID, volatileOnly, goalVolatileInterval) {
			continue
		}
		offers = append(offers, offer{seq: row.HubSeq, id: row.ID, change: watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, change: change,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, ChangeType: change,
				Resource: &hubv1.WatchResponse_Goal{Goal: g},
			},
			match: func(n *filterNode) bool { return nodeMatchesGoal(n, v) },
			was:   was,
		}})
	}
	// One job's writes publish in stamp order (02c "Order"), by id for
	// writes that share one stamp.
	sort.Slice(offers, func(i, j int) bool {
		if offers[i].seq != offers[j].seq {
			return offers[i].seq < offers[j].seq
		}
		return offers[i].id < offers[j].id
	})
	for _, o := range offers {
		f.offerWatchLocked(o.seq, o.change)
	}
}

// publishSessionCreated publishes a hub-started session (CreateSession):
// the queued EnsureSession command as OPERATIONS CREATED first, then the
// session with its create write as SESSIONS CREATED, in ascending stamp
// order. row is the after-image the job captured in its tx. A request_id
// replay publishes nothing: the first call already did. It runs in the
// CreateSession post-commit hook on the writer, in commit order (#393).
func (f *Fleet) publishSessionCreated(ctx context.Context, row statedb.SessionRow, op *hubv1.Operation, opSeq int64, replay bool) {
	if replay {
		return
	}
	// The operation stamps first (outbox.Enqueue takes its hub_seq before
	// the session row takes its own), so it publishes first: ascending
	// stamp order keeps a resume from the first from skipping the second.
	// Each publish takes watchMu separately through offerWatch; the tick
	// carries no audit tail after #403, so nothing interleaves between
	// these two contiguous stamps.
	if op != nil {
		f.publishOperationWatch(opSeq, op)
	}
	sess, err := sessionProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "SESSIONS", "err_kind", "io")
		return
	}
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_SESSIONS, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_SESSIONS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			Resource: &hubv1.WatchResponse_Session{Session: sess},
		},
		match: func(n *filterNode) bool { return nodeMatchesSession(n, row) },
	})
}

// PublishWorkItems publishes a syncer page's work-item changes with the
// stamps the page commit took, in stamp order. Each change carries the
// after-image its job captured in its tx; the recorded before image feeds
// UNMATCHED evaluation. The caller holds no lock.
func (f *Fleet) PublishWorkItems(ctx context.Context, changes []statedb.WorkItemChange) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for _, c := range changes {
		row := c.After
		v, err := workItemViewOf(row)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "WORK_ITEMS", "err_kind", "io")
			continue
		}
		w, err := workItemProto(v)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "WORK_ITEMS", "err_kind", "io")
			continue
		}
		change := hubv1.ChangeType_CHANGE_TYPE_UPDATED
		if c.Created {
			change = hubv1.ChangeType_CHANGE_TYPE_CREATED
		}
		var was func(*filterNode) bool
		if c.Before != nil {
			if bv, err := workItemViewOf(*c.Before); err == nil {
				was = func(n *filterNode) bool { return nodeMatchesWorkItem(n, bv) }
			}
		}
		f.offerWatchLocked(c.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS, change: change,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS, ChangeType: change,
				Resource: &hubv1.WatchResponse_WorkItem{WorkItem: w},
			},
			match: func(n *filterNode) bool { return nodeMatchesWorkItem(n, v) },
			was:   was,
		})
	}
}

// PublishChangeRequests publishes a syncer page's change-request changes,
// as [PublishWorkItems]. The caller holds no lock.
func (f *Fleet) PublishChangeRequests(ctx context.Context, changes []statedb.ChangeRequestChange) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for _, c := range changes {
		row := c.After
		v, err := changeRequestViewOf(row)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "CHANGE_REQUESTS", "err_kind", "io")
			continue
		}
		cr, err := changeRequestProto(v)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "CHANGE_REQUESTS", "err_kind", "io")
			continue
		}
		change := hubv1.ChangeType_CHANGE_TYPE_UPDATED
		if c.Created {
			change = hubv1.ChangeType_CHANGE_TYPE_CREATED
		}
		var was func(*filterNode) bool
		if c.Before != nil {
			if bv, err := changeRequestViewOf(*c.Before); err == nil {
				was = func(n *filterNode) bool { return nodeMatchesChangeRequest(n, bv) }
			}
		}
		f.offerWatchLocked(c.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS, change: change,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS, ChangeType: change,
				Resource: &hubv1.WatchResponse_ChangeRequest{ChangeRequest: cr},
			},
			match: func(n *filterNode) bool { return nodeMatchesChangeRequest(n, v) },
			was:   was,
		})
	}
}

// PanePublication is one agent PaneSummary event the hub publishes: the
// pane key and lines as reported, attributed to the stream's machine and
// the snapshot's session for the pane (empty for the developer's own
// panes), with the machine stamp of the event job that carried it.
type PanePublication struct {
	Pane           *typev1.PaneKey
	MachineID      string
	SessionID      string
	LastLines      string
	LastOutputTime time.Time
	Activity       typev1.Activity
	Seq            int64
}

// PublishPane records the latest summary of one pane (02c PANE_SUMMARIES):
// latest-wins, never logged, so a resume re-sends the current value as
// INITIAL. The version rises per pane so clients dedupe by it. The caller
// holds no lock.
func (f *Fleet) PublishPane(ctx context.Context, p PanePublication) {
	if p.Seq <= 0 {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "PANE_SUMMARIES", "err_kind", "io")
		return
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	summary := &hubv1.PaneSummary{
		Pane: p.Pane, Machine: &typev1.MachineId{Value: p.MachineID},
		Session: &typev1.SessionId{Value: p.SessionID}, LastLines: p.LastLines,
		LastOutputTime: timestamppb.New(p.LastOutputTime), Activity: p.Activity,
		UpdateTime: timestamppb.New(f.now()),
	}
	msg := &hubv1.WatchResponse{
		Topic: hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		Resource: &hubv1.WatchResponse_PaneSummary{PaneSummary: summary},
	}
	key := paneKeyOf(msg)
	f.paneVersions[key]++
	summary.Version = f.paneVersions[key]
	f.watch.publishPane(key, watchEntry{at: f.now(), seq: uint64(p.Seq), change: watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg:   msg,
		match: func(n *filterNode) bool { return nodeMatchesPane(n, summary) },
	}})
}

// publishAuditTail publishes the audit rows committed since the last tail
// position as AUDIT CREATED in hub_seq order, then moves the position.
// Rows the entry assembly rejects log publish_failed and are skipped past,
// so one bad row cannot wedge the tail. The caller holds watchMu.
func (f *Fleet) publishAuditTail(ctx context.Context) {
	rows, err := statedb.ListAuditSince(ctx, f.store, f.auditSeq)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "AUDIT", "err_kind", "io")
		return
	}
	for _, r := range rows {
		entry, err := auditEntryOf(r)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "AUDIT", "err_kind", "io")
			f.auditSeq = r.HubSeq
			continue
		}
		v := auditView{stored: r}
		f.offerWatchLocked(r.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_AUDIT, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_AUDIT, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
				Resource: &hubv1.WatchResponse_AuditEntry{AuditEntry: entry},
			},
			match: func(n *filterNode) bool { return nodeMatchesAudit(n, v) },
		})
		f.auditSeq = r.HubSeq
	}
}

// publishAudits publishes one job's captured audit rows after its resource,
// in hub_seq order. writeOrdered jobs call it inside their post-commit
// hook on the writer: per-job rows are seq-contiguous, so each job's
// resource-then-audits in batch order is total hub_seq order, and a resume
// never skips (#403). It takes watchMu and advances the tick tail's
// position past what it publishes, so the tail never replays them.
func (f *Fleet) publishAudits(rows []statedb.AuditStored) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for _, r := range rows {
		if r.HubSeq <= f.auditSeq {
			continue
		}
		entry, err := auditEntryOf(r)
		if err != nil {
			f.tel.Logger.Error("watch.publish_failed", "topic", "AUDIT", "err_kind", "parse")
			continue
		}
		v := auditView{stored: r}
		f.offerWatchLocked(r.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_AUDIT, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_AUDIT, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
				Resource: &hubv1.WatchResponse_AuditEntry{AuditEntry: entry},
			},
			match: func(n *filterNode) bool { return nodeMatchesAudit(n, v) },
		})
		f.auditSeq = r.HubSeq
	}
}
