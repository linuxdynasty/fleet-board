package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
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
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", before)
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
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", before)
	unmatched := pump.recvLive(t)
	if unmatched.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		unmatched.GetGoal().GetId().GetValue() != "g-new" {
		t.Fatalf("left filter = %v %q, want UNMATCHED g-new",
			unmatched.GetChangeType(), unmatched.GetGoal().GetId().GetValue())
	}
	// An unchanged row publishes nothing.
	e.fleet.PublishGoalsCommitted(context.Background(), "m-goals", goalRowsFor(t, e, "m-goals"))
	pump.expectNone(t)
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
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before)
	primed := pump.recvLive(t)
	if primed.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("primed = %v, want the first change at once", primed.GetChangeType())
	}
	before = goalRowsFor(t, e, "m-vol")
	stampGoal(t, e, "g-vol", "m-vol", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "line two", cur, cur.Add(time.Second))
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before)
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
	e.fleet.PublishGoalsCommitted(context.Background(), "m-vol", before)
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
	omsg := pump.recvLive(t)
	if omsg.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		omsg.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS {
		t.Fatalf("operation = %v %v, want OPERATIONS CREATED", omsg.GetChangeType(), omsg.GetTopic())
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

// A syncer page's inserts and updates reach WORK_ITEMS subscribers with
// the page's stamps, and a row that leaves the filter arrives once as
// UNMATCHED.
func TestWatchSyncWorkItemsPublish(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedWorkMirror(t, e, "first", 100, hubv1.WorkStage_WORK_STAGE_READY)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS, `stage = READY`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS); len(initial) != 1 {
		t.Fatalf("%d INITIAL work items, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	changes := seedWorkMirror(t, e, "second", 110, hubv1.WorkStage_WORK_STAGE_READY)
	if len(changes) != 1 || changes[0].Created {
		t.Fatalf("page changes = %+v, want one update", changes)
	}
	e.fleet.PublishWorkItems(context.Background(), changes)
	updated := pump.recvLive(t)
	if updated.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		updated.GetWorkItem().GetId().GetKey() != "7398" {
		t.Fatalf("mirror = %v %q, want UPDATED 7398",
			updated.GetChangeType(), updated.GetWorkItem().GetId().GetKey())
	}
	//nolint:gosec // G115: page stamps are positive
	if updated.GetHubSeq() != uint64(changes[0].HubSeq) {
		t.Errorf("mirror hub_seq = %d, want the page stamp %d", updated.GetHubSeq(), changes[0].HubSeq)
	}
	// A stage move out of the filter arrives once as UNMATCHED.
	changes = seedWorkMirror(t, e, "second", 120, hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE)
	e.fleet.PublishWorkItems(context.Background(), changes)
	unmatched := pump.recvLive(t)
	if unmatched.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED {
		t.Fatalf("left filter = %v, want UNMATCHED", unmatched.GetChangeType())
	}
}

// Committed audit rows reach AUDIT subscribers through the publish tick,
// in hub_seq order, and a second tick sends nothing new.
func TestWatchAuditTailLive(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_AUDIT, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_AUDIT); len(initial) != 0 {
		t.Fatalf("%d INITIAL audit entries, want none", len(initial))
	}
	pump := pumpWatch(stream)
	at := fleetNow
	if err := statedb.WriteAudit(context.Background(), e.st,
		statedb.AuditEntry{Action: "session.create", Target: "sessions/s-1", Now: at},
		statedb.AuditEntry{Action: "session.keys", Target: "sessions/s-1", Now: at.Add(time.Second)},
	); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	first := pump.recvLive(t)
	second := pump.recvLive(t)
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		first.GetAuditEntry().GetAction() != "session.create" {
		t.Fatalf("first = %v %q, want CREATED session.create",
			first.GetChangeType(), first.GetAuditEntry().GetAction())
	}
	if second.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		second.GetAuditEntry().GetAction() != "session.keys" {
		t.Fatalf("second = %v %q, want CREATED session.keys",
			second.GetChangeType(), second.GetAuditEntry().GetAction())
	}
	if second.GetHubSeq() <= first.GetHubSeq() {
		t.Errorf("second hub_seq %d, want after first %d", second.GetHubSeq(), first.GetHubSeq())
	}
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
}

// nextStamp takes one hub_seq on the test store, so pane publications
// carry a real position.
func nextStamp(t *testing.T, e *fleetEnv, at time.Time) int64 {
	t.Helper()
	var seq int64
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		st, err := statedb.NextStamp(context.Background(), tx, at)
		if err != nil {
			return err
		}
		seq = st.HubSeq
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// Pane summaries publish latest-wins with a rising version: two reports
// of one pane arrive in order, and a resume re-sends the latest as
// INITIAL.
func TestWatchPaneSummariesLive(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES)
	pump := pumpWatch(stream)
	key := &typev1.PaneKey{Server: "default", ServerStartId: "boot-1", PaneId: "%0"}
	at := fleetNow
	e.fleet.PublishPane(context.Background(), api.PanePublication{
		Pane: key, MachineID: "m-1", SessionID: "s-1", LastLines: "one",
		LastOutputTime: at, Activity: typev1.Activity_ACTIVITY_ACTIVE, Seq: nextStamp(t, e, at),
	})
	first := pump.recvLive(t)
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		first.GetPaneSummary().GetVersion() != 1 ||
		first.GetPaneSummary().GetLastLines() != "one" {
		t.Fatalf("first = %v v%d %q, want UPDATED v1 one",
			first.GetChangeType(), first.GetPaneSummary().GetVersion(), first.GetPaneSummary().GetLastLines())
	}
	e.fleet.PublishPane(context.Background(), api.PanePublication{
		Pane: key, MachineID: "m-1", SessionID: "s-1", LastLines: "two",
		LastOutputTime: at.Add(time.Second), Activity: typev1.Activity_ACTIVITY_ACTIVE, Seq: nextStamp(t, e, at.Add(time.Second)),
	})
	second := pump.recvLive(t)
	if second.GetPaneSummary().GetVersion() != 2 ||
		second.GetPaneSummary().GetLastLines() != "two" {
		t.Fatalf("second = v%d %q, want v2 two",
			second.GetPaneSummary().GetVersion(), second.GetPaneSummary().GetLastLines())
	}
	// A resume re-sends the latest summary as INITIAL, not the log.
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	resumed, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, "", true)},
		ResumeToken:   second.GetResumeToken(),
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	initial, _ := drainInitial(t, resumed, hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES)
	if len(initial) != 1 || initial[0].GetPaneSummary().GetLastLines() != "two" {
		t.Fatalf("%d INITIAL panes, want the latest two", len(initial))
	}
}

// clientAs opens a second client over the same Fleet as a different
// caller, so resume tests can change scopes between streams.
func clientAs(t *testing.T, e *fleetEnv, who *typev1.Principal) hubv1connect.FleetServiceClient {
	t.Helper()
	path, h := e.fleet.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(api.WithPrincipal(r.Context(), who))
		h.ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return hubv1connect.NewFleetServiceClient(srv.Client(), srv.URL)
}

// A resume whose caller lost a covered topic's scope ends with
// RESYNC_REQUIRED and SCOPES_CHANGED instead of a refusal; a topic the
// token never covered still refuses.
func TestWatchScopesChangedOnResume(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedMachine(t, e, "m-scopes")
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, done := drainInitial(t, first, hubv1.WatchTopic_WATCH_TOPIC_GOALS)
	token := done.GetResumeToken()
	if token == "" {
		t.Fatal("INITIAL_DONE carries no resume token")
	}
	other := clientAs(t, e, user)
	// The same subscription without its scope resyncs as SCOPES_CHANGED.
	resyncing, err := other.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_GOALS, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	resyncing.Receive()
	msg := resyncing.Msg()
	if msg.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED ||
		msg.GetResyncReason() != hubv1.ResyncReason_RESYNC_REASON_SCOPES_CHANGED {
		t.Fatalf("resume = %v %v, want RESYNC_REQUIRED SCOPES_CHANGED",
			msg.GetChangeType(), msg.GetResyncReason())
	}
	// A topic the token never covered still refuses.
	refused, serr := other.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", false)},
		ResumeToken:   token,
	}))
	if code, reason, _ := detail(t, openWatchErr(refused, serr)); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("uncovered topic = %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}

// The publish tick recomputes the fleet summary once a published change
// dirties it, at most once per second: a second dirty inside the second
// waits for the next tick.
func TestWatchSummaryTickPublishes(t *testing.T) {
	t.Parallel()
	cur := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), withNow(func() time.Time { return cur }))
	seedMachine(t, e, "m-sum")
	stampGoal(t, e, "g-sum", "m-sum", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "work on it", cur, cur)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY); len(initial) != 1 {
		t.Fatalf("%d INITIAL summaries, want 1", len(initial))
	}
	pump := pumpWatch(stream)
	// The enroll's audit row publishes through the tail and dirties the
	// summary with it, so the first tick sends a summary too.
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	first := pump.recvLive(t)
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		first.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY {
		t.Fatalf("summary = %v %v, want UPDATED FLEET_SUMMARY",
			first.GetChangeType(), first.GetTopic())
	}
	// With the dirty cleared the next tick stays silent.
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
	// A second dirty inside the second waits out the throttle.
	e.fleet.PublishMachineState(context.Background(), "m-sum", "ENROLLED")
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	pump.expectNone(t)
	cur = cur.Add(2 * time.Second)
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	second := pump.recvLive(t)
	if second.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY {
		t.Fatalf("summary = %v, want FLEET_SUMMARY after the throttle", second.GetTopic())
	}
}
