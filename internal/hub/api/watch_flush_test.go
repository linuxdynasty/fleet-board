package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file holds the volatile-flush tests: the hold survives a transient
// failure, drops on success or confirmed deletion, and flushes at a fresh
// Watch position newer than every entry published while held.

// A transient flush failure keeps the held machine update: the tick after
// the failure still publishes the coalesced value. Only a success or a
// confirmed permanent deletion drops the hold.
func TestWatchFlushRetainsHeldOnTransientFailure(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-retain")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES); len(initial) != 1 {
		t.Fatalf("%d INITIAL machines, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	e.fleet.PublishMachineState(context.Background(), "m-retain", "ENROLLED")
	pump.recvLive(t)
	before, err := statedb.ReadMachineRow(context.Background(), e.st, "m-retain")
	if err != nil {
		t.Fatal(err)
	}
	touchMachine(t, e, "m-retain", "observed_time = ?", []any{cur.Add(time.Second).UnixMilli()}, cur.Add(time.Second))
	e.fleet.PublishMachineCommitted(context.Background(), "m-retain", &before)
	pump.expectNone(t)
	cur = cur.Add(31 * time.Second)
	// A cancelled store read fails transiently: nothing publishes, and the
	// hold survives for the next tick.
	failed, stop := context.WithCancel(context.Background())
	stop()
	if err := e.fleet.PublishTick(failed); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("flushed = %v, want the held UPDATED after the transient failure", flushed.GetChangeType())
	}
}

// A held goal update survives a transient flush failure the same way.
func TestWatchFlushRetainsHeldGoalOnTransientFailure(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-retain-g")
	stampGoal(t, e, "g-retain", "m-retain-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one", cur, cur)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_GOALS); len(initial) != 1 {
		t.Fatalf("%d INITIAL goals, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	before := goalRowsFor(t, e, "m-retain-g")
	stampGoal(t, e, "g-retain", "m-retain-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one-prime", cur, cur)
	e.fleet.PublishGoalsCommitted(context.Background(), "m-retain-g", before, goalRowsFor(t, e, "m-retain-g"))
	pump.recvLive(t)
	before = goalRowsFor(t, e, "m-retain-g")
	stampGoal(t, e, "g-retain", "m-retain-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line two", cur, cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-retain-g", before, goalRowsFor(t, e, "m-retain-g"))
	pump.expectNone(t)
	cur = cur.Add(6 * time.Second)
	failed, stop := context.WithCancel(context.Background())
	stop()
	if err := e.fleet.PublishTick(failed); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		flushed.GetGoal().GetStatus().GetFirstLine() != "line two" {
		t.Fatalf("flushed = %v %q, want UPDATED with the held first_line",
			flushed.GetChangeType(), flushed.GetGoal().GetStatus().GetFirstLine())
	}
}

// A held machine flush carries a position newer than every entry published
// while it was held: the held row's own stamp went stale behind them, and
// a resume from one of them must still reach this message.
func TestWatchFlushTakesAFreshPosition(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-flush")
	seedMachine(t, e, "m-newer")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES); len(initial) != 2 {
		t.Fatalf("%d INITIAL machines, want 2", len(initial))
	}
	pump := pumpWatch(stream)
	e.fleet.PublishMachineState(context.Background(), "m-flush", "ENROLLED")
	pump.recvLive(t)
	before, err := statedb.ReadMachineRow(context.Background(), e.st, "m-flush")
	if err != nil {
		t.Fatal(err)
	}
	touchMachine(t, e, "m-flush", "observed_time = ?", []any{cur.Add(time.Second).UnixMilli()}, cur.Add(time.Second))
	e.fleet.PublishMachineCommitted(context.Background(), "m-flush", &before)
	pump.expectNone(t)
	// An unrelated newer change moves the token past the held row's stamp:
	// a non-volatile write publishes at once with a fresh stamp.
	newerBefore, err := statedb.ReadMachineRow(context.Background(), e.st, "m-newer")
	if err != nil {
		t.Fatal(err)
	}
	touchMachine(t, e, "m-newer", "name = ?", []any{"renamed"}, cur.Add(2*time.Second))
	e.fleet.PublishMachineCommitted(context.Background(), "m-newer", &newerBefore)
	newer := pump.recvLive(t)
	cur = cur.Add(31 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("flushed = %v, want the held UPDATED", flushed.GetChangeType())
	}
	if flushed.GetHubSeq() <= newer.GetHubSeq() {
		t.Fatalf("flushed seq %d <= newer seq %d, want the flush positioned after it",
			flushed.GetHubSeq(), newer.GetHubSeq())
	}
}

// A held goal flush carries a fresh position the same way.
func TestWatchFlushGoalTakesAFreshPosition(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-flush-g")
	seedMachine(t, e, "m-newer-g")
	stampGoal(t, e, "g-flush", "m-flush-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one", cur, cur)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, "", true),
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", true),
		},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_GOALS)
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	pump := pumpWatch(stream)
	before := goalRowsFor(t, e, "m-flush-g")
	stampGoal(t, e, "g-flush", "m-flush-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one-prime", cur, cur)
	e.fleet.PublishGoalsCommitted(context.Background(), "m-flush-g", before, goalRowsFor(t, e, "m-flush-g"))
	pump.recvLive(t)
	before = goalRowsFor(t, e, "m-flush-g")
	stampGoal(t, e, "g-flush", "m-flush-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line two", cur, cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-flush-g", before, goalRowsFor(t, e, "m-flush-g"))
	pump.expectNone(t)
	newerBefore, err := statedb.ReadMachineRow(context.Background(), e.st, "m-newer-g")
	if err != nil {
		t.Fatal(err)
	}
	touchMachine(t, e, "m-newer-g", "name = ?", []any{"renamed"}, cur.Add(2*time.Second))
	e.fleet.PublishMachineCommitted(context.Background(), "m-newer-g", &newerBefore)
	newer := pump.recvLive(t)
	cur = cur.Add(6 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_GOALS ||
		flushed.GetGoal().GetStatus().GetFirstLine() != "line two" {
		t.Fatalf("flushed = %v %q, want the held GOALS update",
			flushed.GetTopic(), flushed.GetGoal().GetStatus().GetFirstLine())
	}
	if flushed.GetHubSeq() <= newer.GetHubSeq() {
		t.Fatalf("flushed seq %d <= newer seq %d, want the flush positioned after it",
			flushed.GetHubSeq(), newer.GetHubSeq())
	}
}

// A held goal whose row is gone is a confirmed permanent deletion: the
// flush drops the hold and publishes nothing, now or later.
func TestWatchFlushDropsHeldGoalAfterDelete(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-drop-g")
	stampGoal(t, e, "g-drop", "m-drop-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one", cur, cur)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_GOALS); len(initial) != 1 {
		t.Fatalf("%d INITIAL goals, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	before := goalRowsFor(t, e, "m-drop-g")
	stampGoal(t, e, "g-drop", "m-drop-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one-prime", cur, cur)
	e.fleet.PublishGoalsCommitted(context.Background(), "m-drop-g", before, goalRowsFor(t, e, "m-drop-g"))
	pump.recvLive(t)
	before = goalRowsFor(t, e, "m-drop-g")
	stampGoal(t, e, "g-drop", "m-drop-g", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line two", cur, cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-drop-g", before, goalRowsFor(t, e, "m-drop-g"))
	pump.expectNone(t)
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `DELETE FROM goals WHERE id = ?`, "g-drop")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(6 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
}
