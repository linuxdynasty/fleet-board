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

// offerWatch logs one never-dropped change with the store stamp. A
// non-positive seq means the write carried no stamp and stays unpublished.
func (f *Fleet) offerWatch(seq int64, change watchChange) {
	if seq <= 0 {
		return
	}
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

// publishMachineWatch offers a machine write: the row is re-read so live
// filtering sees exactly what ListMachines serves. Replay publishes
// nothing.
func (f *Fleet) publishMachineWatch(ctx context.Context, id string, change hubv1.ChangeType, replay bool) {
	if replay {
		return
	}
	row, err := statedb.ReadMachineRow(ctx, f.store, id)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
	match := func(n *filterNode) bool { return nodeMatchesMachine(n, row) }
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: change,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: change,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: match,
	})
}

// publishTriageWatch offers a triage write with its UPDATED item. Replay
// publishes nothing.
func (f *Fleet) publishTriageWatch(ctx context.Context, stored statedb.TriageStored, item *hubv1.TriageItem) {
	if stored.Replay {
		return
	}
	v := triageView{stored: stored}
	match := func(n *filterNode) bool { return nodeMatchesTriage(n, v) }
	f.offerWatch(stored.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_TriageItem{TriageItem: item},
		},
		match: match,
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
