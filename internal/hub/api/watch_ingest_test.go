package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// watchPump is the single reader of one Watch stream: it owns Receive, so
// an absence check never leaves an orphaned read to steal the next
// message. Start it after drainInitial consumed the initial state.
type watchPump struct {
	ch     chan *hubv1.WatchResponse
	closed chan struct{}
}

func pumpWatch(stream *connect.ServerStreamForClient[hubv1.WatchResponse]) *watchPump {
	p := &watchPump{ch: make(chan *hubv1.WatchResponse, 64), closed: make(chan struct{})}
	go func() {
		defer close(p.closed)
		for stream.Receive() {
			p.ch <- stream.Msg()
		}
	}()
	return p
}

// next returns the next live message, or nil when none arrives in the
// wait (or the server closed the stream).
func (p *watchPump) next(wait time.Duration) *hubv1.WatchResponse {
	select {
	case m := <-p.ch:
		return m
	case <-p.closed:
		return nil
	case <-time.After(wait):
		return nil
	}
}

// recvLive fails when no live message arrives in 10 s.
func (p *watchPump) recvLive(t *testing.T) *hubv1.WatchResponse {
	t.Helper()
	if m := p.next(10 * time.Second); m != nil {
		return m
	}
	t.Fatal("no live Watch message in 10s")
	return nil
}

// expectNone fails when a live message arrives within a short wait;
// silence is the pass.
func (p *watchPump) expectNone(t *testing.T) {
	t.Helper()
	if m := p.next(200 * time.Millisecond); m != nil {
		t.Fatalf("unexpected live message: %v %s", m.GetChangeType(), m.GetTopic())
	}
}

// stampGoal writes one observed goal row with a fresh stamp and returns
// the stamp, so ingest-publish tests control the before image exactly.
// phaseAt is the phase time (kept across same-phase sightings, as the
// applier keeps it); observedAt stamps the write.
func stampGoal(t *testing.T, e *fleetEnv, id, machine string, phase hubv1.GoalPhase, firstLine string,
	phaseAt, observedAt time.Time,
) int64 {
	t.Helper()
	var seq int64
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		st, err := statedb.NextStamp(context.Background(), tx, observedAt)
		if err != nil {
			return err
		}
		status, err := protojson.Marshal(&hubv1.GoalStatus{
			Phase: phase, PhaseReason: hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED,
			PhaseTime: timestamppb.New(phaseAt), LineState: typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			FirstLine: firstLine, StartTime: timestamppb.New(phaseAt),
		})
		if err != nil {
			return err
		}
		seq = st.HubSeq
		return statedb.UpsertObservedGoal(context.Background(), tx, statedb.ObservedRow{
			ID: id, MachineID: machine, Spec: observedGoalSpec(machine, "work on it"),
			Status: string(status), Phase: phase.String(),
			Reason:  hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED.String(),
			StartMs: phaseAt.UnixMilli(), ObservedMs: observedAt.UnixMilli(), HubSeq: st.HubSeq,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func goalRowsFor(t *testing.T, e *fleetEnv, machine string) []statedb.GoalRow {
	t.Helper()
	rows, err := statedb.ListGoalRows(context.Background(), e.st)
	if err != nil {
		t.Fatal(err)
	}
	var out []statedb.GoalRow
	for _, r := range rows {
		if r.MachineID == machine {
			out = append(out, r)
		}
	}
	return out
}

// An ingest job's goal writes reach GOALS subscribers: a new row as
// CREATED, a phase move as UPDATED, a row that leaves the filter as
// UNMATCHED, and an unchanged row as nothing.
func TestWatchIngestGoalsPublishLive(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedMachine(t, e, "m-goals")
	at := fleetNow
	stampGoal(t, e, "g-running", "m-goals", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "work on it", at, at)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, `phase = RUNNING`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_GOALS); len(initial) != 1 {
		t.Fatalf("%d INITIAL goals, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	before := goalRowsFor(t, e, "m-goals")
	stampGoal(t, e, "g-new", "m-goals", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "fresh", at.Add(time.Second), at.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", before, goalRowsFor(t, e, "m-goals"))
	got := pump.recvLive(t)
	if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED || got.GetGoal().GetId().GetValue() != "g-new" {
		t.Fatalf("live = %v %q, want CREATED g-new", got.GetChangeType(), got.GetGoal().GetId().GetValue())
	}
	if got.GetHubSeq() == 0 || got.GetResumeToken() == "" {
		t.Error("live goal change carries no position")
	}
	// A phase move out of the filter arrives once as UNMATCHED.
	before = goalRowsFor(t, e, "m-goals")
	stampGoal(t, e, "g-new", "m-goals", hubv1.GoalPhase_GOAL_PHASE_WAITING, "fresh", at.Add(2*time.Second), at.Add(2*time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", before, goalRowsFor(t, e, "m-goals"))
	unmatched := pump.recvLive(t)
	if unmatched.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		unmatched.GetGoal().GetId().GetValue() != "g-new" {
		t.Fatalf("left filter = %v %q, want UNMATCHED g-new",
			unmatched.GetChangeType(), unmatched.GetGoal().GetId().GetValue())
	}
	// An unchanged row publishes nothing.
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", goalRowsFor(t, e, "m-goals"), goalRowsFor(t, e, "m-goals"))
	pump.expectNone(t)
}

// Each queued goal event publishes its own committed state: with two rapid
// transitions, the first publish carries the intermediate change and the
// second the final one, in stamp order. Re-reading the current row for
// every settlement would publish the final state twice and lose the
// intermediate change.
func TestWatchIngestGoalsPublishEachJobsState(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedMachine(t, e, "m-seq")
	at := fleetNow
	stampGoal(t, e, "g-seq", "m-seq", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "step zero", at, at)
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
	// Two transitions commit before either settles, as up to 256 event
	// jobs may: each settlement publishes its own job's after-image.
	beforeFirst := goalRowsFor(t, e, "m-seq")
	stampGoal(t, e, "g-seq", "m-seq", hubv1.GoalPhase_GOAL_PHASE_WAITING, "step one", at, at.Add(time.Second))
	afterFirst := goalRowsFor(t, e, "m-seq")
	beforeSecond := goalRowsFor(t, e, "m-seq")
	stampGoal(t, e, "g-seq", "m-seq", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "step two", at.Add(time.Second), at.Add(2*time.Second))
	afterSecond := goalRowsFor(t, e, "m-seq")
	e.fleet.PublishGoalsCommitted(context.Background(), "m-seq", beforeFirst, afterFirst)
	e.fleet.PublishGoalsCommitted(context.Background(), "m-seq", beforeSecond, afterSecond)
	first := pump.recvLive(t)
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		first.GetGoal().GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_WAITING ||
		first.GetGoal().GetStatus().GetFirstLine() != "step one" {
		t.Fatalf("first = %v %v %q, want UPDATED WAITING step one",
			first.GetChangeType(), first.GetGoal().GetStatus().GetPhase(), first.GetGoal().GetStatus().GetFirstLine())
	}
	second := pump.recvLive(t)
	if second.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		second.GetGoal().GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_RUNNING ||
		second.GetGoal().GetStatus().GetFirstLine() != "step two" {
		t.Fatalf("second = %v %v %q, want UPDATED RUNNING step two",
			second.GetChangeType(), second.GetGoal().GetStatus().GetPhase(), second.GetGoal().GetStatus().GetFirstLine())
	}
	if first.GetHubSeq() >= second.GetHubSeq() {
		t.Fatalf("seqs %d then %d, want strictly ascending", first.GetHubSeq(), second.GetHubSeq())
	}
}

// touchMachine stamps one machine row with a fresh hub_seq after applying
// the SET fragment, and returns the stamp: a stand-in for the ingest jobs'
// machine writes.
func touchMachine(t *testing.T, e *fleetEnv, id, sets string, args []any, at time.Time) int64 {
	t.Helper()
	var seq int64
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		st, err := statedb.NextStamp(context.Background(), tx, at)
		if err != nil {
			return err
		}
		seq = st.HubSeq
		//nolint:gosec // G202: test-only helper; SET clause is a fixed caller literal, values are bound args
		_, err = tx.ExecContext(context.Background(), `UPDATE machines SET hub_seq = ?, `+sets+` WHERE machine_id = ?`,
			append([]any{st.HubSeq}, append(args, id)...)...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// A volatile-only write waits out its 02c interval: the first_line edit
// below sends nothing until the tick after the 5 s goal interval, and a
// phase move still publishes at once.
func TestWatchIngestGoalsVolatileGate(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-vol")
	stampGoal(t, e, "g-vol", "m-vol", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one", cur, cur)
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
	// A first volatile change publishes at once and starts the interval;
	// a second one inside it is held.
	before := goalRowsFor(t, e, "m-vol")
	stampGoal(t, e, "g-vol", "m-vol", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line one-prime", cur, cur)
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before, goalRowsFor(t, e, "m-vol"))
	primed := pump.recvLive(t)
	if primed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("primed = %v, want the first change at once", primed.GetChangeType())
	}
	before = goalRowsFor(t, e, "m-vol")
	stampGoal(t, e, "g-vol", "m-vol", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line two", cur, cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before, goalRowsFor(t, e, "m-vol"))
	pump.expectNone(t)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
	cur = cur.Add(6 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		flushed.GetGoal().GetStatus().GetFirstLine() != "line two" {
		t.Fatalf("flushed = %v %q, want UPDATED with the latest first_line",
			flushed.GetChangeType(), flushed.GetGoal().GetStatus().GetFirstLine())
	}
	// A phase move is not volatile: it publishes at once.
	before = goalRowsFor(t, e, "m-vol")
	stampGoal(t, e, "g-vol", "m-vol", hubv1.GoalPhase_GOAL_PHASE_WAITING, "line two", cur.Add(time.Second), cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before, goalRowsFor(t, e, "m-vol"))
	moved := pump.recvLive(t)
	if moved.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		moved.GetGoal().GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_WAITING {
		t.Fatalf("moved = %v %v, want UPDATED WAITING at once",
			moved.GetChangeType(), moved.GetGoal().GetStatus().GetPhase())
	}
}

// A machine stamp that changes only volatile fields waits out the 30 s
// machine interval; a state change publishes at once.
func TestWatchIngestMachinesVolatileGate(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-mvol")
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
	// Prime the interval with a state publish, then hold a volatile-only
	// stamp on the second write.
	e.fleet.PublishMachineState(context.Background(), "m-mvol", "ENROLLED")
	primed := pump.recvLive(t)
	if primed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("primed = %v, want the state change at once", primed.GetChangeType())
	}
	before, err := statedb.ReadMachineRow(context.Background(), e.st, "m-mvol")
	if err != nil {
		t.Fatal(err)
	}
	touchMachine(t, e, "m-mvol", "observed_time = ?", []any{cur.Add(time.Second).UnixMilli()}, cur.Add(time.Second))
	e.fleet.PublishMachineCommitted(context.Background(), "m-mvol", &before)
	pump.expectNone(t)
	cur = cur.Add(31 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	flushed := pump.recvLive(t)
	if flushed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("flushed = %v, want the held UPDATED", flushed.GetChangeType())
	}
}

// CreateSession publishes its hub-started session as SESSIONS CREATED and
// its queued command as OPERATIONS CREATED; a request_id replay publishes
// neither.
func TestWatchCreateSessionPublishes(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	enrollMachine(t, e, "m-sess")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_SESSIONS, "", true),
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, "", true),
		},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	for _, topic := range []hubv1.WatchTopic{
		hubv1.WatchTopic_WATCH_TOPIC_SESSIONS, hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS,
	} {
		// An idle bus positions INITIAL_DONE at zero; the live messages
		// below prove the position.
		drainInitial(t, stream, topic)
	}
	pump := pumpWatch(stream)
	sess := createSession(t, e, "m-sess", "work")
	// The operation stamps first (outbox.Enqueue) and the session second
	// (NextStamp), so the stream carries OPERATIONS before SESSIONS in
	// ascending stamp order: a resume from the first never skips the
	// second.
	omsg := pump.recvLive(t)
	if omsg.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		omsg.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS {
		t.Fatalf("operation = %v %v, want OPERATIONS CREATED", omsg.GetChangeType(), omsg.GetTopic())
	}
	smsg := pump.recvLive(t)
	if smsg.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		smsg.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_SESSIONS ||
		smsg.GetSession().GetId().GetValue() != sess {
		t.Fatalf("session = %v %v %q, want SESSIONS CREATED %q",
			smsg.GetChangeType(), smsg.GetTopic(), smsg.GetSession().GetId().GetValue(), sess)
	}
	if smsg.GetHubSeq() == 0 || smsg.GetResumeToken() == "" {
		t.Error("live session carries no position")
	}
	if omsg.GetHubSeq() == 0 || omsg.GetHubSeq() >= smsg.GetHubSeq() {
		t.Fatalf("operation seq %d session seq %d, want strictly ascending", omsg.GetHubSeq(), smsg.GetHubSeq())
	}
	// The replay of the same request_id publishes nothing new.
	createSession(t, e, "m-sess", "work")
	pump.expectNone(t)
}

// seedWorkMirror stores one provider mirror and returns the page's
// reported changes, so sync-publish tests drive the production path.
func seedWorkMirror(t *testing.T, e *fleetEnv, title string, updateMs int64, stage hubv1.WorkStage) []statedb.WorkItemChange {
	t.Helper()
	item := &integrationv1.WorkItem{
		Ref: &typev1.WorkItemRef{Key: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web",
			Key:         "7398",
		}},
		Title:      title,
		State:      integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		UpdateTime: timestamppb.New(workBase.Add(time.Duration(updateMs) * time.Millisecond)),
	}
	raw, err := protojson.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := statedb.UpsertWorkItemPage(context.Background(), e.st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "WORK_ITEM_KIND_ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON: raw, UpdateMs: updateMs, Stage: int32(stage),
			TrustJSON: []byte(`{"trusted":true}`),
		}}, nil, nil, updateMs)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}
