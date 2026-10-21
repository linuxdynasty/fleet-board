package api

import (
	"context"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the Watch publish tick (02c): the housekeeping job that
// keeps the live topics no commit hook can keep alone. Every run holds
// only watchMu (the commitWatch discipline):
//
//   - the audit tail publishes the audit rows committed anywhere since
//     the last position (gateway, syncer, housekeeping expiry — every
//     writer job appends its rows, so no single commit hook sees them
//     all) as AUDIT CREATED in hub_seq order;
//   - the volatile flush re-reads the held volatile-only changes whose
//     interval passed and publishes the latest values;
//   - the summary recompute publishes the fleet summary when a published
//     change dirtied it, at most once per second.
//
// A run never fails: a store or assembly error logs its static event and
// the next run retries. The caller holds no lock.

// summaryPublishInterval paces FLEET_SUMMARY recomputes (02c: at most one
// per second).
const summaryPublishInterval = watchSummaryInterval

// PublishTick runs one publish pass: the audit tail, the volatile flush
// and the dirty summary, in hub_seq order. It always returns nil; every
// failure is logged and retried on the next pass.
func (f *Fleet) PublishTick(ctx context.Context) error {
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	f.publishAuditTail(ctx)
	f.flushVolatile(ctx)
	f.publishSummaryTick(ctx)
	return nil
}

// flushVolatile publishes the held volatile-only changes whose interval
// passed, re-read so the message carries the latest values. The caller
// holds watchMu.
func (f *Fleet) flushVolatile(ctx context.Context) {
	now := f.now()
	for key, held := range f.volatile.held {
		var interval time.Duration
		switch held.topic {
		case hubv1.WatchTopic_WATCH_TOPIC_MACHINES:
			interval = machineVolatileInterval
		case hubv1.WatchTopic_WATCH_TOPIC_GOALS:
			interval = goalVolatileInterval
		default:
			delete(f.volatile.held, key)
			continue
		}
		if now.Sub(f.volatile.last[gateKey(held.topic, held.id)]) < interval {
			continue
		}
		switch held.topic {
		case hubv1.WatchTopic_WATCH_TOPIC_MACHINES:
			f.flushMachine(ctx, held.id, key)
		case hubv1.WatchTopic_WATCH_TOPIC_GOALS:
			f.flushGoal(ctx, held.id, key)
		}
	}
}

// flushMachine publishes one held machine change with its current row.
// The caller holds watchMu.
func (f *Fleet) flushMachine(ctx context.Context, id, key string) {
	row, err := statedb.ReadMachineRow(ctx, f.store, id)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		delete(f.volatile.held, key)
		return
	}
	m, err := machineProto(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		delete(f.volatile.held, key)
		return
	}
	delete(f.volatile.held, key)
	f.volatile.last[key] = f.now()
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Machine{Machine: m},
		},
		match: func(n *filterNode) bool { return nodeMatchesMachine(n, row) },
	})
}

// flushGoal publishes one held goal change with its current row. The
// caller holds watchMu.
func (f *Fleet) flushGoal(ctx context.Context, id, key string) {
	row, err := statedb.ReadGoalRow(ctx, f.store, id)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
		delete(f.volatile.held, key)
		return
	}
	v, err := goalViewOf(row)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
		delete(f.volatile.held, key)
		return
	}
	g, err := goalProto(v)
	if err != nil {
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
		delete(f.volatile.held, key)
		return
	}
	delete(f.volatile.held, key)
	f.volatile.last[key] = f.now()
	f.offerWatch(row.HubSeq, watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_Goal{Goal: g},
		},
		match: func(n *filterNode) bool { return nodeMatchesGoal(n, v) },
	})
}

// publishSummaryTick recomputes the fleet summary when a published change
// dirtied it, at most once per interval, and records it as the latest for
// live streams and resumes. The caller holds watchMu.
func (f *Fleet) publishSummaryTick(ctx context.Context) {
	if !f.summaryDirty {
		return
	}
	now := f.now()
	if now.Sub(f.summaryAt) < summaryPublishInterval {
		return
	}
	summary, step, err := f.buildFleetSummary(ctx)
	if err != nil {
		kind := "io"
		f.tel.Logger.ErrorContext(ctx, "summary.failed", "step", step, "err_kind", kind, "actor", "tick")
		return
	}
	f.summaryDirty = false
	f.summaryAt = now
	f.watch.publishSummary(watchEntry{at: now, seq: f.summarySeq, change: watchChange{
		topic: hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		msg: &hubv1.WatchResponse{
			Topic: hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			Resource: &hubv1.WatchResponse_FleetSummary{FleetSummary: summary},
		},
	}})
}
