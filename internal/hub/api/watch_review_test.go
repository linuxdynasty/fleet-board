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

// watchWait bounds one live Watch message: failures surface in seconds,
// not at the 10 s helper default.
const watchWait = 5 * time.Second

// dismissCall dismisses one triage item like snoozeCall snoozes one.
func dismissCall(t *testing.T, e *fleetEnv, secret, reqID, id string, version uint64) {
	t.Helper()
	_, err := e.client.DismissTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.DismissTriageItemRequest{
		RequestId: reqID, Id: &hubv1.TriageItemId{Value: id},
		ExpectedVersion: version, Note: "handled at the machine",
	}), secret))
	if err != nil {
		t.Fatalf("DismissTriageItem: %v", err)
	}
}

// Phase-1 red tests for the #390 review findings: each compiles against the
// current code and fails at runtime until its finding is fixed.

// A machine rename that leaves the subscription filter must arrive once as
// UNMATCHED with its current state (P1-1), not vanish silently.
func TestWatchMachineRenameLeavesFilter(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, `name = "laptop"`, true)},
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
	left, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no UNMATCHED after the rename left the filter")
	}
	if left.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		left.GetMachine().GetSpec().GetDisplayName() != "renamed" {
		t.Fatalf("left filter = %v %q, want UNMATCHED renamed",
			left.GetChangeType(), left.GetMachine().GetSpec().GetDisplayName())
	}
}

// A triage snooze that leaves the subscription filter must arrive once as
// UNMATCHED with its current state (P1-1), not vanish silently.
func TestWatchTriageSnoozeLeavesFilter(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, `state = OPEN`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_TRIAGE); len(initial) != 1 {
		t.Fatalf("%d INITIAL triage items, want 1", len(initial))
	}
	snoozeCall(t, e, "", "sn-1", "ti-1", 1, timestamppb.New(fleetNow.Add(2*time.Hour)))
	left, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no UNMATCHED after the snooze left the filter")
	}
	if left.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		left.GetTriageItem().GetState() != hubv1.TriageState_TRIAGE_STATE_SNOOZED {
		t.Fatalf("left filter = %v %v, want UNMATCHED SNOOZED",
			left.GetChangeType(), left.GetTriageItem().GetState())
	}
}

// A triage dismiss that leaves the subscription filter must arrive once as
// UNMATCHED with its current state (P1-1), not vanish silently.
func TestWatchTriageDismissLeavesFilter(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-1")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, `state = OPEN`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_TRIAGE); len(initial) != 1 {
		t.Fatalf("%d INITIAL triage items, want 1", len(initial))
	}
	dismissCall(t, e, "", "dm-1", "ti-1", 1)
	left, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no UNMATCHED after the dismiss left the filter")
	}
	if left.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		left.GetTriageItem().GetState() != hubv1.TriageState_TRIAGE_STATE_DISMISSED {
		t.Fatalf("left filter = %v %v, want UNMATCHED DISMISSED",
			left.GetChangeType(), left.GetTriageItem().GetState())
	}
}

// An account delete publishes DELETED with the last state, and a resume
// across it replays the delete (P1-2).
func TestWatchAccountDeletePublishesDeleted(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	created := createOwnerAccount(t, e, "req-1", accountSpec())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS)
	token := done.GetResumeToken()
	if _, err := e.client.DeleteAccount(t.Context(), connect.NewRequest(&hubv1.DeleteAccountRequest{
		RequestId: "req-2", Id: created.GetId(), ExpectedVersion: 1,
	})); err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	gone, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no DELETED after DeleteAccount")
	}
	if gone.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_DELETED ||
		gone.GetAccount().GetId().GetValue() != created.GetId().GetValue() {
		t.Fatalf("delete = %v %q, want DELETED %q",
			gone.GetChangeType(), gone.GetAccount().GetId().GetValue(), created.GetId().GetValue())
	}
	cancel()
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	stream2, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch resume: %v", err)
	}
	replay, ok := recvWatchAllowClose(t, stream2, watchWait)
	if !ok {
		t.Fatal("no replayed DELETED after resume")
	}
	if replay.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_DELETED ||
		replay.GetHubSeq() != gone.GetHubSeq() {
		t.Fatalf("replay = %v/%d, want DELETED/%d",
			replay.GetChangeType(), replay.GetHubSeq(), gone.GetHubSeq())
	}
}

// A machine revoke that leaves the subscription filter must arrive once
// as UNMATCHED with its current state (P1-1), not vanish silently.
func TestWatchMachineRevokeLeavesFilter(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, `state = ENROLLED`, true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if initial, _ := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES); len(initial) != 1 {
		t.Fatalf("%d INITIAL machines, want 1", len(initial))
	}
	if _, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-1", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatalf("RevokeMachine: %v", err)
	}
	left, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no UNMATCHED after the revoke left the filter")
	}
	if left.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UNMATCHED ||
		left.GetMachine().GetStatus().GetState() != hubv1.MachineState_MACHINE_STATE_REVOKED {
		t.Fatalf("left filter = %v %v, want UNMATCHED REVOKED",
			left.GetChangeType(), left.GetMachine().GetStatus().GetState())
	}
}

// An idle resume with send_initial=false and nothing to replay keeps the
// accepted resume position: the first CHECKPOINT names the snapshot
// sequence, not sequence zero (P2-7). The next resume from that
// checkpoint token replays only what came after it.
func TestWatchIdleResumeKeepsPosition(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false), withWatchCheckpoint(20*time.Millisecond))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	ctx, cancel := context.WithCancel(t.Context())
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_REPOS)
	token := done.GetResumeToken()
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
	checkpoint := recvWatch(t, stream2)
	if checkpoint.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CHECKPOINT {
		t.Fatalf("idle resume = %v, want CHECKPOINT", checkpoint.GetChangeType())
	}
	createWatchedRepo(t, e, "r-2", "github.com", "acme/web")
	ctx3, cancel3 := context.WithCancel(t.Context())
	defer cancel3()
	stream3, err := e.client.Watch(ctx3, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   checkpoint.GetResumeToken(),
	}))
	if err != nil {
		t.Fatalf("Watch resume from checkpoint: %v", err)
	}
	first, ok := recvWatchAllowClose(t, stream3, watchWait)
	if !ok {
		t.Fatal("checkpoint-token resume replayed nothing")
	}
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		first.GetRepo().GetId().GetValue() != "github.com/acme/web" {
		t.Fatalf("replay = %v %q, want CREATED github.com/acme/web (no replay of r-1)",
			first.GetChangeType(), first.GetRepo().GetId().GetValue())
	}
}

// A machine delete publishes DELETED with the last state, and a resume
// across it replays the delete (P1-2).
func TestWatchMachineDeletePublishesDeleted(t *testing.T) {
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
	_, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	token := done.GetResumeToken()
	if _, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-1", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatalf("RevokeMachine: %v", err)
	}
	revoked, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok || revoked.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_UPDATED {
		t.Fatalf("revoke = %v, want UPDATED", revoked.GetChangeType())
	}
	if _, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-2", Id: &typev1.MachineId{Value: id}, ExpectedVersion: 2,
	})); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	gone, ok := recvWatchAllowClose(t, stream, watchWait)
	if !ok {
		t.Fatal("no DELETED after DeleteMachine")
	}
	if gone.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_DELETED ||
		gone.GetMachine().GetId().GetValue() != id {
		t.Fatalf("delete = %v %q, want DELETED %q",
			gone.GetChangeType(), gone.GetMachine().GetId().GetValue(), id)
	}
	if gone.GetHubSeq() <= revoked.GetHubSeq() {
		t.Errorf("delete hub_seq %d, want after revoke %d", gone.GetHubSeq(), revoked.GetHubSeq())
	}
	cancel()
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	stream2, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch resume: %v", err)
	}
	var sawDeleted bool
	for range 2 {
		m, ok := recvWatchAllowClose(t, stream2, watchWait)
		if !ok {
			t.Fatalf("resume replay ended early, want UPDATED then DELETED")
		}
		if m.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_DELETED {
			sawDeleted = true
			if m.GetHubSeq() != gone.GetHubSeq() {
				t.Errorf("replay delete seq = %d, want %d", m.GetHubSeq(), gone.GetHubSeq())
			}
		}
	}
	if !sawDeleted {
		t.Error("resume replay missed the DELETED")
	}
}

// A token below the oldest retained entry is still honoured when nothing
// was evicted: hub_seq gaps are normal, so the log covers it (P2-8).
func TestWatchGapTokenBelowOldestReplays(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	// Enroll stamps hub_seq without publishing, so the first published
	// change starts above the token the first stream mints.
	seedMachine(t, e, "01jmachine0000000000000002")
	seedGitHub(t, e)
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
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	live := recvWatch(t, stream)
	if live.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED {
		t.Fatalf("live = %v, want CREATED", live.GetChangeType())
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
	replay, ok := recvWatchAllowClose(t, stream2, watchWait)
	if !ok {
		t.Fatal("gap token ended the stream without replay")
	}
	if replay.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED {
		if replay.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
			replay.GetHubSeq() != live.GetHubSeq() {
			t.Fatalf("replay = %v/%d, want CREATED/%d",
				replay.GetChangeType(), replay.GetHubSeq(), live.GetHubSeq())
		}
		return
	}
	t.Fatalf("gap token = RESYNC_REQUIRED/%v, want the CREATED replay", replay.GetResyncReason())
}

// Young entries survive past the count cap: the log keeps the larger of
// the time and count guarantees (P2-9).
func TestWatchKeepsYoungEntriesPastCountCap(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false), withWatchLogCap(4))
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
	for i := 2; i <= 7; i++ {
		createWatchedRepo(t, e, "r-"+string(rune('0'+i)), "github.com", "acme/api"+string(rune('0'+i)))
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
	first, ok := recvWatchAllowClose(t, stream2, watchWait)
	if !ok {
		t.Fatal("young-token resume ended the stream without replay")
	}
	if first.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED {
		t.Fatalf("young entries evicted by count alone = RESYNC_REQUIRED/%v, want the replay",
			first.GetResyncReason())
	}
	if first.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED {
		t.Fatalf("replay = %v, want CREATED", first.GetChangeType())
	}
}
