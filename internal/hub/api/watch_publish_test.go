package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Repo writes publish live: an update inside the filter arrives as
// UPDATED, one that leaves the filter as UNMATCHED with its current
// state, and a delete as DELETED carrying the last state.
func TestWatchRepoWritesPublishLive(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, `enabled = true`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_REPOS); done.GetHubSeq() == 0 {
		t.Fatal("INITIAL_DONE hub_seq is 0")
	}
	disable := repoRequest("r-1", "github.com", "acme/api")
	disable.GetSpec().Enabled = false
	updateWatchedRepo(t, e, "r-2", disable.GetSpec(), []string{"spec.enabled"}, 1)
	left := recvWatch(t, stream)
	if left.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		left.GetRepo().GetId().GetValue() != "github.com/acme/api" {
		t.Fatalf("left filter = %v %q, want UNMATCHED github.com/acme/api",
			left.GetChangeType(), left.GetRepo().GetId().GetValue())
	}
	if left.GetRepo().GetSpec().GetEnabled() {
		t.Error("UNMATCHED carries the stale enabled state")
	}
	enable := repoRequest("r-1", "github.com", "acme/api")
	enable.GetSpec().Enabled = true
	updateWatchedRepo(t, e, "r-3", enable.GetSpec(), []string{"spec.enabled"}, 2)
	back := recvWatch(t, stream)
	if back.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
		back.GetRepo().GetId().GetValue() != "github.com/acme/api" {
		t.Fatalf("re-entry = %v %q, want UPDATED github.com/acme/api",
			back.GetChangeType(), back.GetRepo().GetId().GetValue())
	}
	if back.GetHubSeq() <= left.GetHubSeq() {
		t.Errorf("re-entry hub_seq %d, want after UNMATCHED %d", back.GetHubSeq(), left.GetHubSeq())
	}
}

// Every other wired write publishes live with its store stamp: a machine
// rename, a triage snooze, an account create and a settings update each
// arrive as CREATED or UPDATED on their topic.
func TestWatchWritesPublishLive(t *testing.T) {
	t.Parallel()
	t.Run("machine update", func(t *testing.T) {
		t.Parallel()
		adm := &testMachineAdmin{revoker: &fakeRevoker{}}
		e := newMachineEnv(t, localUser("alice"), adm)
		id := "01jmachine0000000000000001"
		seedMachine(t, e, id)
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
		if _, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
			updateMachineRequest(id, "req-1", "renamed", []string{"desk"}, 1,
				[]string{"spec.display_name", "spec.labels"}))); err != nil {
			t.Fatalf("UpdateMachine: %v", err)
		}
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
			got.GetMachine().GetSpec().GetDisplayName() != "renamed" {
			t.Fatalf("machine = %v %q, want UPDATED renamed",
				got.GetChangeType(), got.GetMachine().GetSpec().GetDisplayName())
		}
		if got.GetHubSeq() == 0 || got.GetResumeToken() == "" {
			t.Error("live machine change carries no position")
		}
	})
	t.Run("triage snooze", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"), trusted(false))
		seedTriageItem(t, e, "ti-1")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_TRIAGE); len(initial) != 1 {
			t.Fatalf("%d INITIAL triage items, want 1", len(initial))
		}
		until := timestamppb.New(fleetNow.Add(2 * time.Hour))
		snoozeCall(t, e, "", "sn-1", "ti-1", 1, until)
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
			got.GetTriageItem().GetState() != hubv1.TriageState_TRIAGE_STATE_SNOOZED {
			t.Fatalf("triage = %v %v, want UPDATED SNOOZED",
				got.GetChangeType(), got.GetTriageItem().GetState())
		}
	})
	t.Run("account create", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"), trusted(false))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS); len(initial) != 0 {
			t.Fatalf("%d INITIAL accounts, want 0", len(initial))
		}
		created := createOwnerAccount(t, e, "req-1", accountSpec())
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
			got.GetAccount().GetId().GetValue() != created.GetId().GetValue() {
			t.Fatalf("account = %v %q, want CREATED %q",
				got.GetChangeType(), got.GetAccount().GetId().GetValue(), created.GetId().GetValue())
		}
	})
	t.Run("settings update", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"), trusted(false))
		maxActive := uint32(2)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_SETTINGS, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_SETTINGS)
		updateFleetSettings(t, e, "req-1", &hubv1.SettingsSpec{
			Scope:  fleetScope(),
			Values: &typev1.SettingValues{MaxActive: &maxActive},
			Hub:    &hubv1.HubSettings{AutoDispatch: func() *bool { b := true; return &b }()},
		}, []string{"spec.values.max_active", "spec.hub.auto_dispatch"}, 0)
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
			got.GetSettings().GetSpec().GetValues().GetMaxActive() != 2 {
			t.Fatalf("settings = %v max_active %d, want UPDATED 2",
				got.GetChangeType(), got.GetSettings().GetSpec().GetValues().GetMaxActive())
		}
	})
}

// Queued commands, decided approvals and integration writes publish
// live on their topics.
func TestWatchCommandsApprovalsIntegrationsPublishLive(t *testing.T) {
	t.Parallel()
	t.Run("sendkeys operation", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"), trusted(false))
		enrollMachine(t, e, "m1")
		sess := createSession(t, e, "m1", "work")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS); len(initial) != 0 {
			t.Fatalf("%d INITIAL operations, want 0", len(initial))
		}
		res, err := e.client.SendKeys(t.Context(), connect.NewRequest(sendKeysReq(sess, "%1")))
		if err != nil {
			t.Fatalf("SendKeys: %v", err)
		}
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
			got.GetOperation().GetId().GetValue() != res.Msg.GetOperation().GetId().GetValue() {
			t.Fatalf("operation = %v %q, want CREATED %q",
				got.GetChangeType(), got.GetOperation().GetId().GetValue(),
				res.Msg.GetOperation().GetId().GetValue())
		}
	})
	t.Run("decided approval", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"))
		hexSecret := secretEnv(t, e)
		enrollMachine(t, e, "m1")
		sess := createSession(t, e, "m1", "work")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_APPROVALS, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_APPROVALS)
		gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
		if err != nil {
			t.Fatal(err)
		}
		id := gated.Msg.GetApproval().GetValue()
		created := recvWatch(t, stream)
		if created.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED {
			t.Fatalf("approval raise = %v, want CREATED", created.GetChangeType())
		}
		if _, err := e.client.DecideApproval(context.Background(), withSecret(connect.NewRequest(
			&hubv1.DecideApprovalRequest{
				RequestId:       "req-decide-1",
				Id:              &hubv1.ApprovalId{Value: id},
				ExpectedVersion: 1,
				Decision:        "approve",
			}), hexSecret)); err != nil {
			t.Fatalf("DecideApproval: %v", err)
		}
		decided := recvWatch(t, stream)
		if decided.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
			decided.GetApproval().GetState() != hubv1.ApprovalState_APPROVAL_STATE_DECIDED {
			t.Fatalf("approval = %v %v, want UPDATED DECIDED",
				decided.GetChangeType(), decided.GetApproval().GetState())
		}
	})
	t.Run("integration update", func(t *testing.T) {
		t.Parallel()
		e := newFleetEnv(t, localUser("alice"), trusted(false))
		seedGitHub(t, e)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
			Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS, "", true)},
		}))
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS); len(initial) != 1 {
			t.Fatalf("%d INITIAL integrations, want 1", len(initial))
		}
		updateIntegration(t, e, "req-1", "github",
			&hubv1.IntegrationSpec{Label: "GitHub"}, []string{"spec.label"}, 0)
		got := recvWatch(t, stream)
		if got.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED ||
			got.GetIntegration().GetSpec().GetLabel() != "GitHub" {
			t.Fatalf("integration = %v %q, want UPDATED GitHub",
				got.GetChangeType(), got.GetIntegration().GetSpec().GetLabel())
		}
	})
}

// A delete publishes DELETED with the last state at a new position, so a
// resume across it replays the delete instead of missing it.
func TestWatchRepoDeletePublishesDeleted(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_REPOS)
	token := done.GetResumeToken()
	if _, err := e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: "r-2", Id: &hubv1.RepoId{Value: "github.com/acme/api"}, ExpectedVersion: 1,
	})); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	gone := recvWatch(t, stream)
	if gone.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_DELETED ||
		gone.GetRepo().GetId().GetValue() != "github.com/acme/api" {
		t.Fatalf("delete = %v %q, want DELETED github.com/acme/api",
			gone.GetChangeType(), gone.GetRepo().GetId().GetValue())
	}
	if gone.GetHubSeq() <= done.GetHubSeq() {
		t.Errorf("delete hub_seq %d, want after initial %d", gone.GetHubSeq(), done.GetHubSeq())
	}
	cancel()
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	stream2, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch resume: %v", err)
	}
	replay := recvWatch(t, stream2)
	if replay.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_DELETED ||
		replay.GetHubSeq() != gone.GetHubSeq() {
		t.Fatalf("replay = %v/%d, want DELETED/%d",
			replay.GetChangeType(), replay.GetHubSeq(), gone.GetHubSeq())
	}
}
