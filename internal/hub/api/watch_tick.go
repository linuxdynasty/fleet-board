package api

import (
	"context"
	"errors"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the Watch publish tick (02c): the housekeeping job that
// keeps the live topics no commit hook can keep alone. The summary holds
// only watchMu; each volatile flush releases it while its restamp
// commits, publishing in the commit hook in commit order (#393):
//
//   - the volatile flush restamps the held volatile-only changes whose
//     interval passed and publishes the latest values at the fresh
//     position, so a resume never skips them;
//   - the summary recompute publishes the fleet summary when a published
//     change dirtied it, at most once per second.
//
// Audit rows need no tail: every writer job publishes its own audits in
// its post-commit hook, after its resource, in batch order, so one
// writer-serialized sequence covers both in hub_seq order (#403).
//
// A run never fails: a store or assembly error logs its static event and
// the next run retries. The caller holds no lock.

// summaryPublishInterval paces FLEET_SUMMARY recomputes (02c: at most one
// per second).
const summaryPublishInterval = watchSummaryInterval

// PublishTick runs one publish pass: the volatile flush and the dirty
// summary. It always returns nil; every failure is logged and retried on
// the next pass. The volatile flush releases watchMu while each restamp
// commits: a flush holds no lock across the writer, so a commit hook
// publishing under watchMu can never wedge against the tick (#393).
func (f *Fleet) PublishTick(ctx context.Context) error {
	f.watchMu.Lock()
	due := f.dueVolatile(f.now())
	f.watchMu.Unlock()
	for _, d := range due {
		switch d.topic {
		case hubv1.WatchTopic_WATCH_TOPIC_MACHINES:
			f.flushMachine(ctx, d.id, d.key)
		case hubv1.WatchTopic_WATCH_TOPIC_GOALS:
			f.flushGoal(ctx, d.id, d.key)
		default:
			// Unreachable: dueVolatile keeps only MACHINES and GOALS.
		}
	}
	f.watchMu.Lock()
	defer f.watchMu.Unlock()
	f.publishSummaryTick(ctx)
	return nil
}

// volatileDue is one held volatile-only change whose interval passed.
type volatileDue struct {
	topic   hubv1.WatchTopic
	id, key string
}

// dueVolatile lists the held volatile-only changes whose interval passed,
// pruning holds for unknown topics. The caller holds watchMu.
func (f *Fleet) dueVolatile(now time.Time) []volatileDue {
	var due []volatileDue
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
		due = append(due, volatileDue{topic: held.topic, id: held.id, key: key})
	}
	return due
}

// flushMachine publishes one held machine change with its current row,
// restamped to a position newer than every entry published while it was
// held: the held row's own stamp went stale behind them, and a resume
// from one of them must not skip this message. The restamp commits on the
// writer with the publish in its commit hook, so the message lands in
// commit order with every other hook publish (#393). A retryable failure
// keeps the hold for the next tick; only a success or a confirmed
// permanent deletion (the row is gone) drops it. The caller holds no lock:
// the hook takes watchMu briefly to publish, and a flush never holds
// watchMu across the writer.
func (f *Fleet) flushMachine(ctx context.Context, id, key string) {
	_, err := statedb.RestampMachine(ctx, f.store, id, f.now(), func(row statedb.MachineRow) {
		f.watchMu.Lock()
		defer f.watchMu.Unlock()
		if _, ok := f.volatile.held[key]; !ok {
			return // a later flush already published; skip the duplicate
		}
		m, err := machineProto(row)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
			return
		}
		delete(f.volatile.held, key)
		f.volatile.last[key] = f.now()
		f.offerWatchLocked(row.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
				Resource: &hubv1.WatchResponse_Machine{Machine: m},
			},
			match: func(n *filterNode) bool { return nodeMatchesMachine(n, row) },
		})
	})
	if err != nil {
		if errors.Is(err, statedb.ErrMachineUnknown) {
			f.watchMu.Lock()
			delete(f.volatile.held, key)
			f.watchMu.Unlock()
			return
		}
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "MACHINES", "err_kind", "io")
		return
	}
}

// flushGoal publishes one held goal change with its current row,
// restamped to a position newer than every entry published while it was
// held, as flushMachine does for machines. The restamp commits on the
// writer with the publish in its commit hook, so the message lands in
// commit order (#393). A retryable failure keeps the hold for the next
// tick; only a success or a confirmed permanent deletion (the row is
// gone) drops it. The caller holds no lock.
func (f *Fleet) flushGoal(ctx context.Context, id, key string) {
	_, err := statedb.RestampGoal(ctx, f.store, id, f.now(), func(row statedb.GoalRow) {
		f.watchMu.Lock()
		defer f.watchMu.Unlock()
		if _, ok := f.volatile.held[key]; !ok {
			return // a later flush already published; skip the duplicate
		}
		v, err := goalViewOf(row)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
			return
		}
		g, err := goalProto(v)
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
			return
		}
		delete(f.volatile.held, key)
		f.volatile.last[key] = f.now()
		f.offerWatchLocked(row.HubSeq, watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
				Resource: &hubv1.WatchResponse_Goal{Goal: g},
			},
			match: func(n *filterNode) bool { return nodeMatchesGoal(n, v) },
		})
	})
	if err != nil {
		if errors.Is(err, statedb.ErrGoalUnknown) {
			f.watchMu.Lock()
			delete(f.volatile.held, key)
			f.watchMu.Unlock()
			return
		}
		f.tel.Logger.ErrorContext(ctx, "watch.publish_failed", "topic", "GOALS", "err_kind", "io")
		return
	}
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
