package api

import (
	"context"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file offers committed writes to Watch streams (02c). Every publish
// runs after the commit, never in it: a rolled-back job publishes nothing,
// and a publish failure never fails the RPC (the change is already
// committed; the next resume replays it from the log). Idempotent replays
// publish nothing: the first commit already did.
//
// Commit order is the publish order: the store stamps hub_seq in commit
// order, and every publish runs in a post-commit hook on the single
// writer goroutine, so the hook for commit N always runs before the hook
// for commit N+1 and the bus appends in commit order (02c "Order", #393).
// A hook publishes only its own commit's rows: the resource, then the
// audits its tx stamped, so one writer-serialized sequence covers both in
// hub_seq order. Every publish takes watchMu briefly through offerWatch
// and never waits on the writer; no path holds watchMu across a writer
// job, so the discipline cannot wedge (#403).

// offerWatch logs one never-dropped change with the store stamp. It takes
// watchMu; callers that already hold it use offerWatchLocked. A
// non-positive seq means the write carried no stamp and stays unpublished.
// Every published change dirties the fleet summary: the publish tick
// recomputes it at most once per second (02c).
func (f *Fleet) offerWatch(seq int64, change watchChange) {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	f.offerWatchLocked(seq, change)
}

// offerWatchLocked logs one never-dropped change as offerWatch does. The
// caller holds watchMu, so a hook's several publishes stay atomic and a
// concurrent tick cannot interleave between them.
func (f *Fleet) offerWatchLocked(seq int64, change watchChange) {
	if seq <= 0 {
		return
	}
	if u := uint64(seq); u > f.summarySeq {
		f.summarySeq = u
	}
	f.summaryDirty = true
	f.watch.publish(watchEntry{at: f.now(), seq: uint64(seq), change: change})
}

// publishRepoWatch offers a watched-repo write. oldSpec is the pre-update
// spec JSON for UNMATCHED evaluation, nil when unknown; replayed writes
// publish nothing.
func (f *Fleet) publishRepoWatch(ctx context.Context, stored statedb.RepoStored, change hubv1.ChangeType, oldSpec []byte, replay bool) {
	if replay {
		return
	}
	v, err := repoViewOf(stored)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "REPOS", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesRepo(n, v) }
	var was func(*filterNode) bool
	if oldSpec != nil {
		old := stored
		old.Spec = oldSpec
		if ov, err := repoViewOf(old); err == nil {
			was = func(n *filterNode) bool { return nodeMatchesRepo(n, ov) }
		}
	}
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, change: change,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, ChangeType: change,
			Resource: &hubv1.WatchResponse_Repo{Repo: repoFromStored(stored, v.spec)},
		},
		match: match, was: was,
	})
}

// machineOldRow links a pre-write read to its write: the old row is
// exact only when its version is the write's predecessor, so a
// concurrent change that slipped between the read and the commit reads
// unknown instead of stale.
func machineOldRow(pre *statedb.MachineRow, stored statedb.MachineStored) *statedb.MachineRow {
	//nolint:gosec // G115: versions start at 1 and only rise
	if pre != nil && pre.Version+1 == int64(stored.Version) {
		return pre
	}
	return nil
}

// publishMachineWatchRow offers a machine write from its already-read
// rows: row is the new state, old the pre-update state for UNMATCHED
// evaluation, nil when unknown.
func (f *Fleet) publishMachineWatchRow(ctx context.Context, row statedb.MachineRow, change hubv1.ChangeType, old *statedb.MachineRow) {
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesMachine(n, row) }
	var was func(*filterNode) bool
	if old != nil {
		was = func(n *filterNode) bool { return nodeMatchesMachine(n, *old) }
	}
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: change,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: change,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: match, was: was,
	})
}

// publishMachineDeleted offers a machine delete with the last state: the
// row is gone, so the caller passes it with the delete's fresh stamp.
// Replay publishes nothing.
func (f *Fleet) publishMachineDeleted(ctx context.Context, row statedb.MachineRow, hubSeq int64, replay bool) {
	if replay {
		return
	}
	row.HubSeq = hubSeq
	f.publishMachineWatchRow(ctx, row, hubv1.ChangeType_CHANGE_TYPE_DELETED, nil)
}

// triageOldItem links a pre-write read to its write, as [machineOldRow]:
// exact only when its version is the write's predecessor.
func triageOldItem(pre *statedb.TriageStored, stored statedb.TriageStored) *statedb.TriageStored {
	if pre != nil && pre.Version+1 == stored.Version {
		return pre
	}
	return nil
}

// publishTriageWatch offers a triage write with its UPDATED item. old is
// the pre-write item for UNMATCHED evaluation, nil when unknown; replay
// publishes nothing.
func (f *Fleet) publishTriageWatch(ctx context.Context, stored statedb.TriageStored, item *hubv1.TriageItem, old *statedb.TriageStored) {
	if stored.Replay {
		return
	}
	v := triageView{stored: stored}
	match := func(n *filterNode) bool { return nodeMatchesTriage(n, v) }
	var was func(*filterNode) bool
	if old != nil {
		ov := triageView{stored: *old}
		was = func(n *filterNode) bool { return nodeMatchesTriage(n, ov) }
	}
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_TriageItem{TriageItem: item},
		},
		match: match, was: was,
	})
}

// publishAccountWatch offers an account write. oldSpec is the pre-update
// spec JSON, nil when unknown; replay publishes nothing.
func (f *Fleet) publishAccountWatch(ctx context.Context, stored statedb.AccountStored, change hubv1.ChangeType,
	oldSpec []byte, replay bool,
) {
	if replay {
		return
	}
	v, err := accountViewOf(stored)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "ACCOUNTS", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesAccount(n, v) }
	var was func(*filterNode) bool
	if oldSpec != nil {
		old := stored
		old.Spec = oldSpec
		if ov, err := accountViewOf(old); err == nil {
			was = func(n *filterNode) bool { return nodeMatchesAccount(n, ov) }
		}
	}
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, change: change,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, ChangeType: change,
			Resource: &hubv1.WatchResponse_Account{Account: accountFromStored(stored, v.spec)},
		},
		match: match, was: was,
	})
}

// publishSettingsWatch offers the fleet settings write. oldSpec is the
// pre-update spec, nil when unknown; replay publishes nothing.
func (f *Fleet) publishSettingsWatch(ctx context.Context, stored statedb.SettingsStored, oldSpec []byte, replay bool) {
	if replay {
		return
	}
	layer := statedb.SettingsLayerStored{
		Scope: statedb.FleetScope, Spec: stored.Spec,
		Version: stored.Version, CreateMs: stored.CreateMs, UpdateMs: stored.UpdateMs, HubSeq: stored.HubSeq,
	}
	v, err := settingsViewOf(layer)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "SETTINGS", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesSettings(n, v) }
	var was func(*filterNode) bool
	if oldSpec != nil {
		old := layer
		old.Spec = oldSpec
		if ov, err := settingsViewOf(old); err == nil {
			was = func(n *filterNode) bool { return nodeMatchesSettings(n, ov) }
		}
	}
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_SETTINGS, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_SETTINGS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Settings{Settings: settingsFromLayer(v.stored, v.spec)},
		},
		match: match, was: was,
	})
}

// publishIntegrationWatch offers an integration write. oldSpec is the
// pre-update spec, nil when unknown; replay publishes nothing.
func (f *Fleet) publishIntegrationWatch(ctx context.Context, stored statedb.IntegrationStored, oldSpec []byte, replay bool) {
	if replay {
		return
	}
	v, err := integrationViewOf(stored)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "INTEGRATIONS", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesIntegration(n, v) }
	var was func(*filterNode) bool
	if oldSpec != nil {
		old := stored
		old.Spec = oldSpec
		if ov, err := integrationViewOf(old); err == nil {
			was = func(n *filterNode) bool { return nodeMatchesIntegration(n, ov) }
		}
	}
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Integration{
				Integration: integrationFromStored(v.stored, v.spec, v.status),
			},
		},
		match: match, was: was,
	})
}

// publishOperationWatch offers a queued session command as an OPERATIONS
// CREATED. Watch subscriptions to operations take no filter, so every
// stream gets it.
func (f *Fleet) publishOperationWatch(seq int64, op *hubv1.Operation) {
	f.offerWatch(seq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED,
			Resource: &hubv1.WatchResponse_Operation{Operation: op},
		},
	})
}

// publishApprovalWatch offers an approval write: a raise as CREATED, a
// decision as UPDATED. Replay publishes nothing.
func (f *Fleet) publishApprovalWatch(seq int64, approval *hubv1.Approval, change hubv1.ChangeType, replay bool) {
	if replay {
		return
	}
	f.offerWatch(seq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_APPROVALS, change: change,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_APPROVALS, ChangeType: change,
			Resource: &hubv1.WatchResponse_Approval{Approval: approval},
		},
	})
}
