package api_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Mirror and tick publishing: the syncer pages, the audit tail, pane
// summaries and the dirty-summary tick. The stream harness (watchPump,
// newFleetEnv, seed helpers) lives in watch_ingest_test.go.

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
