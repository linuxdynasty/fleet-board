package daemon_test

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// openWatch opens a Watch stream on the running hub's local API with one
// subscription per topic, all with send_initial.
func openWatch(t *testing.T, r *running, topics ...hubv1.WatchTopic,
) (*connect.ServerStreamForClient[hubv1.WatchResponse], context.CancelFunc) {
	t.Helper()
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	subs := make([]*hubv1.TopicSubscription, 0, len(topics))
	for _, topic := range topics {
		subs = append(subs, &hubv1.TopicSubscription{Topic: topic, SendInitial: true})
	}
	ctx, stop := context.WithCancel(t.Context())
	stream, err := client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{Subscriptions: subs}))
	if err != nil {
		stop()
		t.Fatalf("Watch = %v, want a served stream", err)
	}
	return stream, stop
}

// nextWatch reads one message within the timeout.
func nextWatch(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse],
) *hubv1.WatchResponse {
	t.Helper()
	got := make(chan *hubv1.WatchResponse, 1)
	go func() {
		defer close(got)
		if stream.Receive() {
			got <- stream.Msg()
		}
	}()
	select {
	case m, ok := <-got:
		if !ok {
			t.Fatalf("Watch closed: %v", stream.Err())
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no Watch message within 10s")
		return nil
	}
}

// drainInitial reads until every topic reports INITIAL_DONE.
func drainInitial(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse], topics ...hubv1.WatchTopic) {
	t.Helper()
	done := map[hubv1.WatchTopic]bool{}
	for len(done) < len(topics) {
		m := nextWatch(t, stream)
		switch m.GetChangeType() {
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL_DONE:
			done[m.GetTopic()] = true
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL:
		default:
			t.Fatalf("drain got %v %v, want initial state", m.GetChangeType(), m.GetTopic())
		}
	}
}

// An enroll commit on the agent listener reaches a Watch subscriber: the
// daemon wires the gateway's WatchSink to the Fleet in serve.
func TestRunPublishesGatewayEnrollToWatch(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	r := h.start(t, h.options())
	defer r.stop(t)
	stream, stop := openWatch(t, r, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	defer stop()
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	enrollOverRun(t, h, r)
	m := nextWatch(t, stream)
	if m.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_MACHINES ||
		m.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED || m.GetMachine() == nil {
		t.Errorf("live = %v %v machine=%v, want MACHINES CREATED with the enrolled machine",
			m.GetChangeType(), m.GetTopic(), m.GetMachine() != nil)
	}
}

// The housekeeping watch_publish job tails audit rows that no commitWatch
// covers: the gateway enroll job writes its machine.enroll audit row
// through the store writer, and it goes live only after a driven tick.
func TestRunWatchPublishTickTailsAudit(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	ticks := make(chan time.Time)
	opts.HousekeepingTicks = ticks
	r := h.start(t, opts)
	defer r.stop(t)
	stream, stop := openWatch(t, r, hubv1.WatchTopic_WATCH_TOPIC_AUDIT)
	defer stop()
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_AUDIT)
	enrollOverRun(t, h, r)
	ticks <- time.Time{}
	ticks <- time.Time{} // the second send returns once the first check finished
	m := nextWatch(t, stream)
	if m.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_AUDIT ||
		m.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED || m.GetAuditEntry() == nil {
		t.Errorf("live = %v %v audit=%v, want AUDIT CREATED with the enroll entry",
			m.GetChangeType(), m.GetTopic(), m.GetAuditEntry() != nil)
	}
}

// seedDispatchState inserts the repo, account and trusted item a manual
// dispatch needs, before the hub starts (the store is idle until then).
func seedDispatchState(t *testing.T, h *hub) {
	t.Helper()
	st, err := statedb.Open(context.Background(), h.state, h.tel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES ('github.com', 'acme/web', 'github', ?, 1, 1, 1, 1)`,
			`{"enabled":true,"consent":{"acts":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]}}`); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `INSERT INTO accounts(id, harness, provider, display_name,
			origin, spec_json, version, create_ms, update_ms) VALUES ('a1', 'claude', 'anthropic', 'a1', 'OWNER_CREATED', ?, 1, 1, 1)`,
			`{"consent":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.UpsertWorkItemPage(context.Background(), st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "7398",
			ItemJSON:  []byte(`{"title":"fix the leak"}`),
			UpdateMs:  10,
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(`{"trusted":true}`),
		}}, nil, nil, 10, nil); err != nil {
		t.Fatal(err)
	}
}

// A REGISTRY sighting through a running hub drives the dispatched goal
// and publishes both writes: the daemon wires the registry applier into
// the ingest jobs and the Watch sink to the Fleet. The goal moves
// STARTING to RUNNING on WORKING, and the move's goal and work-item
// after-images reach their Watch topics.
func TestRunRegistrySightingPublishesGoalAndWorkItem(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	seedDispatchState(t, h)
	r := h.start(t, h.options())
	defer r.stop(t)
	stream, stop := openWatch(t, r,
		hubv1.WatchTopic_WATCH_TOPIC_MACHINES,
		hubv1.WatchTopic_WATCH_TOPIC_GOALS,
		hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS)
	defer stop()
	drainInitial(t, stream,
		hubv1.WatchTopic_WATCH_TOPIC_MACHINES,
		hubv1.WatchTopic_WATCH_TOPIC_GOALS,
		hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS)
	res, cert := enrollOverRun(t, h, r)
	mid := res.GetMachineId().GetValue()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bs := agentClient(t, h, r, cert).Connect(ctx)
	defer func() { _ = bs.CloseResponse() }()
	if err := bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		MachineId: &typev1.MachineId{Value: mid}, ProtoMin: 1, ProtoMax: 1,
		BootId: "boot-1", InstallId: "install-1", Reason: agentv1.ConnectReason_CONNECT_REASON_START,
	}}}); err != nil {
		t.Fatal(err)
	}
	if m, err := bs.Receive(); err != nil || m.GetHelloAck().GetEpoch() != 1 {
		t.Fatalf("first answer %v, %v; want HelloAck epoch 1", m, err)
	}
	harnesses := []*agentv1.HarnessInfo{{
		Harness: &typev1.Harness{Name: "claude"}, Version: "1.0.0", LaunchAllowed: true,
		GoalControl: &typev1.GoalControl{PauseMode: typev1.GoalPauseMode_GOAL_PAUSE_MODE_INTERRUPT},
	}}
	if err := bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: &agentv1.Snapshot{
		Seq: 5, SnapshotId: "s", Facts: &agentv1.MachineFacts{Hostname: "laptop", Os: "darwin"},
		Policy:    &agentv1.PolicyInfo{Hash: "h1"},
		Harnesses: harnesses,
	}}}); err != nil {
		t.Fatal(err)
	}
	waitMachineOnline(t, h, stream, mid)
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	start, err := client.StartGoal(ctx, connect.NewRequest(&hubv1.StartGoalRequest{
		RequestId: "req-start-1",
		AcknowledgedWarnings: []typev1.ReasonCode{
			typev1.ReasonCode_REASON_CODE_ACCOUNT_USAGE_UNKNOWN,
			typev1.ReasonCode_REASON_CODE_INTEGRATION_LAG,
		},
		Input: &hubv1.GoalSpecInput{
			WorkItem: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: "github-1"},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
				Project:     "acme/web", Key: "7398",
			},
			Repo:     &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
			GoalText: "fix the leak", Model: "opus",
			Placement: &hubv1.GoalPlacement{
				Machine: &typev1.MachineId{Value: mid},
				Harness: &typev1.Harness{Name: "claude"},
				Account: &typev1.AccountId{Value: "a1"},
			},
		},
	}))
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	goalID := start.Msg.GetGoal().GetValue()
	if err := bs.Send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Snapshot{Snapshot: &agentv1.Snapshot{
		Seq: 6, SnapshotId: "s", Facts: &agentv1.MachineFacts{Hostname: "laptop", Os: "darwin"},
		Policy:    &agentv1.PolicyInfo{Hash: "h1"},
		Harnesses: harnesses,
		Goals: []*agentv1.GoalState{{
			GoalId: &typev1.GoalId{Value: goalID}, Binding: agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
			State: typev1.GoalLineState_GOAL_LINE_STATE_WORKING, FirstLine: "working the leak", HeadCommit: "abc123",
		}},
	}}}); err != nil {
		t.Fatal(err)
	}
	waitGoalRunning(t, stream, goalID)
}

// waitMachineOnline reads Watch until the machine reports ONLINE.
func waitMachineOnline(t *testing.T, h *hub, stream *connect.ServerStreamForClient[hubv1.WatchResponse], mid string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m := nextWatch(t, stream)
		if m.GetTopic() == hubv1.WatchTopic_WATCH_TOPIC_MACHINES &&
			m.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_UPDATED &&
			m.GetMachine().GetId().GetValue() == mid &&
			m.GetMachine().GetStatus().GetState() == hubv1.MachineState_MACHINE_STATE_ONLINE {
			return
		}
	}
	t.Fatalf("no ONLINE update for %s within 30s; hub log tail:\n%s", mid, h.logs.String())
}

// waitGoalRunning reads Watch until the goal reports RUNNING and its work
// item reports the RUNNING stage: the registry pass and both publishes.
func waitGoalRunning(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse], goalID string) {
	t.Helper()
	var goal, item bool
	var seen []string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && (!goal || !item) {
		m := nextWatch(t, stream)
		seen = append(seen, m.GetTopic().String()+"/"+m.GetChangeType().String())
		if m.GetTopic() == hubv1.WatchTopic_WATCH_TOPIC_GOALS &&
			m.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_UPDATED &&
			m.GetGoal().GetId().GetValue() == goalID &&
			m.GetGoal().GetStatus().GetPhase() == hubv1.GoalPhase_GOAL_PHASE_RUNNING {
			goal = true
		}
		// Work-item publishes carry the item key, never the goal link
		// (the read path joins the goal from the dispatch rows), so the
		// RUNNING item is the seeded mirror by key, not by goal id.
		if m.GetTopic() == hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS &&
			m.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_UPDATED &&
			m.GetWorkItem().GetId().GetIntegration().GetValue() == "github-1" &&
			m.GetWorkItem().GetId().GetProject() == "acme/web" &&
			m.GetWorkItem().GetId().GetKey() == "7398" &&
			m.GetWorkItem().GetStage() == hubv1.WorkStage_WORK_STAGE_RUNNING {
			item = true
		}
	}
	if !goal || !item {
		t.Fatalf("goal=%v item=%v after 30s, want the RUNNING goal and its RUNNING item; saw %v", goal, item, seen)
	}
}
