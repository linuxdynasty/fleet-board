package api

import (
	"context"
	"slices"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file publishes machine-driven (ingest) commits to Watch streams
// (02c): the snapshot and event jobs (goals, machines, pane summaries),
// the syncer pages (work items, change requests) and hub-started sessions.
// Every method runs after the commit and holds only watchMu (the
// commitWatch discipline): a rolled-back job publishes nothing, and
// concurrent publishers serialize in one place. In-package writes keep
// using commitWatch; these hooks cover the commits other packages make.
//
// reads re-read the row List serves, so live filtering can never disagree
// with the initial state; offerWatch drops a non-positive stamp, so rows
// from before their stamp migration never publish as live.

// volatileGates holds volatile-only changes inside their 02c interval: at
// most one Watch message per resource per interval, the message carrying
// the latest values. A change to any other field publishes at once and
// carries the held values with it. The caller holds watchMu.
type volatileGates struct {
	last map[string]time.Time
	held map[string]heldEntry
}

// heldEntry is a volatile-only change waiting out its interval; the tick
// flushes it by re-reading the row, so the message carries the latest
// values.
type heldEntry struct {
	topic hubv1.WatchTopic
	id    string
}

func newVolatileGates() *volatileGates {
	return &volatileGates{last: map[string]time.Time{}, held: map[string]heldEntry{}}
}

// gateKey names one gated resource.
func gateKey(topic hubv1.WatchTopic, id string) string {
	return topic.String() + "/" + id
}

// allow reports whether the change publishes now. A non-volatile change
// always does (and clears anything held); a volatile-only one does when
// its interval passed since the last publish, else it is held for the
// tick to flush. The caller holds watchMu.
func (f *Fleet) volatileAllow(topic hubv1.WatchTopic, id string, volatileOnly bool, interval time.Duration) bool {
	if !volatileOnly {
		delete(f.volatile.held, gateKey(topic, id))
		f.volatile.last[gateKey(topic, id)] = f.now()
		return true
	}
	key := gateKey(topic, id)
	if f.now().Sub(f.volatile.last[key]) >= interval {
		delete(f.volatile.held, key)
		f.volatile.last[key] = f.now()
		return true
	}
	f.volatile.held[key] = heldEntry{topic: topic, id: id}
	return false
}

// machineVolatileInterval is the 02c MACHINE volatile interval.
const machineVolatileInterval = 30 * time.Second

// goalVolatileInterval is the 02c GOAL volatile interval.
const goalVolatileInterval = 5 * time.Second

// machineVolatileOnly reports whether the machine write touched only
// volatile fields (02c): the telemetry sample, last-seen and counter
// columns and the stamps. State, identity, drain and policy report at
// once.
func machineVolatileOnly(before, after statedb.MachineRow) bool {
	return before.State == after.State && before.Name == after.Name &&
		slices.Equal(before.Labels, after.Labels) &&
		before.Drained == after.Drained && before.AgentDraining == after.AgentDraining &&
		before.InstallID == after.InstallID && before.Facts == after.Facts &&
		before.Version == after.Version && before.CreatedMillis == after.CreatedMillis &&
		before.PolicyHash == after.PolicyHash && before.PolicyError == after.PolicyError &&
		before.PolicyChanged == after.PolicyChanged && before.PolicySafe == after.PolicySafe
}

// goalVolatileOnly reports whether the goal write touched only volatile
// fields (02c: first_line, head_commit, usage_consumed) or the stamps: the
// two views with the volatile status fields masked compare equal and the
// specs match. A view that does not parse is not volatile-only: the change
// publishes at once and the offer logs the data error.
func goalVolatileOnly(before, after goalView) bool {
	mask := func(v goalView) *hubv1.GoalStatus {
		out, _ := proto.Clone(v.status).(*hubv1.GoalStatus)
		if out == nil {
			return nil
		}
		out.FirstLine, out.HeadCommit, out.UsageConsumed = "", "", nil
		out.ObservedTime, out.HubSeq = nil, 0
		return out
	}
	maskedBefore, maskedAfter := mask(before), mask(after)
	if maskedBefore == nil || maskedAfter == nil {
		return false
	}
	return proto.Equal(maskedBefore, maskedAfter) && before.row.Spec == after.row.Spec
}

// PublishMachineState publishes a committed machines.state change (02c):
// ONLINE from a Snapshot job, STALE or OFFLINE from housekeeping, OFFLINE
// from a stream close, ONLINE from a Heartbeat promotion. from is the state
// the job left, for UNMATCHED evaluation. A state change is never
// volatile: it publishes at once. The caller holds no lock.
func (f *Fleet) PublishMachineState(ctx context.Context, machineID, from string) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	after, err := statedb.ReadMachineRow(ctx, f.store, machineID)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	m, err := machineProto(after)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	was := after
	was.State = from
	f.volatile.last[gateKey(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, machineID)] = f.now()
	f.offerWatch(after.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, after) },
		was:   func(n *filterNode) bool { return nodeMatchesMachine(n, was) },
	})
}

// PublishMachineCreated publishes an enrolled machine with its create
// write. Replays publish nothing: the first enroll already did.
func (f *Fleet) PublishMachineCreated(ctx context.Context, machineID string, replay bool) {
	if replay {
		return
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	row, err := statedb.ReadMachineRow(ctx, f.store, machineID)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	f.volatile.last[gateKey(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, machineID)] = f.now()
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, row) },
	})
}

// PublishMachineCommitted publishes a Snapshot job's machine write with
// its before image for UNMATCHED evaluation. before is nil when the caller
// did not read one (or the machine is new to the hub); volatile-only
// writes wait out their interval.
func (f *Fleet) PublishMachineCommitted(ctx context.Context, machineID string, before *statedb.MachineRow) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	after, err := statedb.ReadMachineRow(ctx, f.store, machineID)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	volatileOnly := before != nil && machineVolatileOnly(*before, after)
	if !f.volatileAllow(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, machineID, volatileOnly, machineVolatileInterval) {
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
	f.offerWatch(after.HubSeq, watchChange{
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
// interval behind the gate. The caller holds no lock.
func (f *Fleet) PublishMachineEvent(ctx context.Context, machineID string) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	after, err := statedb.ReadMachineRow(ctx, f.store, machineID)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	if !f.volatileAllow(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, machineID, true, machineVolatileInterval) {
		return
	}
	m, err := machineProto(after)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	f.offerWatch(after.HubSeq, watchChange{
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
// as the commit found them; rows it no longer stamps are untouched. A
// goal whose view does not parse logs publish_failed and skips only that
// goal. The caller holds no lock.
func (f *Fleet) PublishGoalsCommitted(ctx context.Context, machineID string, before []statedb.GoalRow) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	rows, err := statedb.ListGoalRows(ctx, f.store)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
		return
	}
	old := map[string]statedb.GoalRow{}
	for _, b := range before {
		old[b.ID] = b
	}
	type offer struct {
		seq    int64
		change watchChange
	}
	var offers []offer
	for _, row := range rows {
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
		offers = append(offers, offer{seq: row.HubSeq, change: watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, change: change,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, ChangeType: change,
				Resource: &hubv1.WatchResponse_Goal{Goal: g},
			},
			match: func(n *filterNode) bool { return nodeMatchesGoal(n, v) },
			was:   was,
		}})
	}
	// One job's writes publish in stamp order (02c "Order").
	sort.Slice(offers, func(i, j int) bool { return offers[i].seq < offers[j].seq })
	for _, o := range offers {
		f.offerWatch(o.seq, o.change)
	}
}

// publishSessionCreated publishes a hub-started session (CreateSession)
// with its create write, and the queued EnsureSession command as an
// OPERATIONS CREATED. A request_id replay publishes nothing: the first
// call already did. The caller holds watchMu (it runs in the
// CreateSession commitWatch publish step, which serializes commit and
// publish as one ordered step).
func (f *Fleet) publishSessionCreated(ctx context.Context, sessID string, op *hubv1.Operation, opSeq int64, replay bool) {
	if replay {
		return
	}
	row, err := statedb.ReadSessionRow(ctx, f.store, sessID)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "SESSIONS", "err_kind", "io")
		return
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
	if op != nil {
		f.publishOperationWatch(opSeq, op)
	}
}

// PublishWorkItems publishes a syncer page's work-item changes with the
// stamps the page commit took, in stamp order. The rows are re-read so
// live filtering sees what List serves; the recorded before image feeds
// UNMATCHED evaluation. The caller holds no lock.
func (f *Fleet) PublishWorkItems(ctx context.Context, changes []statedb.WorkItemChange) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	for _, c := range changes {
		row, err := statedb.ReadWorkItemRow(ctx, f.store, c.Key)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "WORK_ITEMS", "err_kind", "io")
			continue
		}
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
		f.offerWatch(c.HubSeq, watchChange{
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
		row, err := statedb.ReadChangeRequestRow(ctx, f.store, c.Key)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "CHANGE_REQUESTS", "err_kind", "io")
			continue
		}
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
		f.offerWatch(c.HubSeq, watchChange{
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
	//nolint:gosec // G115: per-pane versions start at 1 and only rise
	f.paneVersions[key]++
	summary.Version = f.paneVersions[key]
	//nolint:gosec // G115: stamps are positive (checked above)
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
		f.offerWatch(r.HubSeq, watchChange{
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
