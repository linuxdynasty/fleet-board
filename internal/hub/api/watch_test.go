package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// recvWatch reads one Watch message or fails. It never returns on a closed
// stream: use recvWatchAllowClose when the server may close.
func recvWatch(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse]) *hubv1.WatchResponse {
	t.Helper()
	msg, ok := recvWatchAllowClose(t, stream, 10*time.Second)
	if !ok {
		t.Fatalf("stream closed: %v", stream.Err())
	}
	return msg
}

func recvWatchAllowClose(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse], timeout time.Duration,
) (*hubv1.WatchResponse, bool) {
	t.Helper()
	type got struct {
		msg *hubv1.WatchResponse
		ok  bool
	}
	ch := make(chan got, 1)
	go func() {
		ok := stream.Receive()
		var m *hubv1.WatchResponse
		if ok {
			m = stream.Msg()
		}
		ch <- got{m, ok}
	}()
	select {
	case g := <-ch:
		return g.msg, g.ok
	case <-time.After(timeout):
		t.Fatalf("no Watch message in %v", timeout)
		return nil, false
	}
}

// openWatchErr opens a Watch stream and returns the open error: a nil return
// with serr == nil means the request was accepted and the caller owns the
// stream. Request-level refusals surface either from Watch or from the first
// Receive.
func openWatchErr(stream *connect.ServerStreamForClient[hubv1.WatchResponse], serr error) error {
	if serr != nil {
		return serr
	}
	stream.Receive()
	err := stream.Err()
	_ = stream.Close()
	return err
}

func watchSub(topic hubv1.WatchTopic, filter string, initial bool) *hubv1.TopicSubscription {
	return &hubv1.TopicSubscription{Topic: topic, Filter: filter, SendInitial: initial}
}

// drainInitial collects INITIAL resources and the INITIAL_DONE for one topic.
func drainInitial(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse], topic hubv1.WatchTopic,
) (initial []*hubv1.WatchResponse, done *hubv1.WatchResponse) {
	t.Helper()
	for {
		m := recvWatch(t, stream)
		if m.GetChangeType() == hubv1.ChangeType_CHANGE_TYPE_INITIAL_DONE {
			if m.GetTopic() != topic {
				t.Fatalf("INITIAL_DONE for %v, want %v", m.GetTopic(), topic)
			}
			return initial, m
		}
		if m.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_INITIAL || m.GetTopic() != topic {
			t.Fatalf("got %v/%v, want INITIAL/%v", m.GetChangeType(), m.GetTopic(), topic)
		}
		initial = append(initial, m)
	}
}

func watchRepoIDs(msgs []*hubv1.WatchResponse) map[string]bool {
	out := map[string]bool{}
	for _, m := range msgs {
		out[m.GetRepo().GetId().GetValue()] = true
	}
	return out
}

// Watch serves the REPOS initial state, then live changes, and a reopen with
// the INITIAL_DONE token replays what the first stream saw live.
func TestWatchResumeAfterReconnect(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	ctx := t.Context()
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	createWatchedRepo(t, e, "r-2", "github.com", "acme/web")

	ctx1, cancel1 := context.WithCancel(ctx)
	stream1, err := e.client.Watch(ctx1, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	initial, done := drainInitial(t, stream1, hubv1.WatchTopic_WATCH_TOPIC_REPOS)
	if ids := watchRepoIDs(initial); !ids["github.com/acme/api"] || !ids["github.com/acme/web"] || len(ids) != 2 {
		t.Fatalf("initial repos = %v, want api and web", ids)
	}
	if done.GetResumeToken() == "" {
		t.Fatal("INITIAL_DONE carries no resume_token")
	}
	if done.GetHubSeq() == 0 {
		t.Error("INITIAL_DONE hub_seq is 0")
	}

	createWatchedRepo(t, e, "r-3", "github.com", "acme/cli")
	live := recvWatch(t, stream1)
	if live.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		live.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_REPOS ||
		live.GetRepo().GetId().GetValue() != "github.com/acme/cli" {
		t.Fatalf("live = %v/%v %q, want CREATED/REPOS github.com/acme/cli",
			live.GetChangeType(), live.GetTopic(), live.GetRepo().GetId().GetValue())
	}
	if live.GetHubSeq() <= done.GetHubSeq() {
		t.Errorf("live hub_seq %d, want after initial %d", live.GetHubSeq(), done.GetHubSeq())
	}
	if live.GetResumeToken() == "" {
		t.Error("live message carries no resume_token")
	}
	cancel1()

	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	stream2, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   done.GetResumeToken(),
	}))
	if err != nil {
		t.Fatalf("Watch resume: %v", err)
	}
	replay := recvWatch(t, stream2)
	if replay.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		replay.GetRepo().GetId().GetValue() != "github.com/acme/cli" {
		t.Fatalf("replay = %v %q, want CREATED github.com/acme/cli",
			replay.GetChangeType(), replay.GetRepo().GetId().GetValue())
	}
	if replay.GetHubSeq() != live.GetHubSeq() {
		t.Errorf("replay hub_seq %d, want the live seq %d", replay.GetHubSeq(), live.GetHubSeq())
	}
	createWatchedRepo(t, e, "r-4", "github.com", "acme/docs")
	more := recvWatch(t, stream2)
	if more.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED ||
		more.GetRepo().GetId().GetValue() != "github.com/acme/docs" {
		t.Fatalf("second live = %v %q, want CREATED github.com/acme/docs",
			more.GetChangeType(), more.GetRepo().GetId().GetValue())
	}
	if more.GetHubSeq() <= replay.GetHubSeq() {
		t.Errorf("second live hub_seq %d, want after replay %d", more.GetHubSeq(), replay.GetHubSeq())
	}
}

// A resume token from another hub instance ends the stream with
// RESYNC_REQUIRED/HUB_RESTARTED, and the close is clean.
func TestWatchTokenFromAnotherHubNeedsResync(t *testing.T) {
	t.Parallel()
	a := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, a)
	createWatchedRepo(t, a, "r-1", "github.com", "acme/api")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := a.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, done := drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_REPOS)
	token := done.GetResumeToken()

	b := newFleetEnv(t, localUser("alice"), trusted(false), withInstanceID("hub-b"))
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	stream2, err := b.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch on second hub: %v", err)
	}
	resync, ok := recvWatchAllowClose(t, stream2, 10*time.Second)
	if !ok {
		t.Fatalf("stream closed without RESYNC_REQUIRED: %v", stream2.Err())
	}
	if resync.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED ||
		resync.GetResyncReason() != hubv1.ResyncReason_RESYNC_REASON_HUB_RESTARTED {
		t.Errorf("got %v/%v, want RESYNC_REQUIRED/HUB_RESTARTED",
			resync.GetChangeType(), resync.GetResyncReason())
	}
	if resync.GetResource() != nil {
		t.Error("RESYNC_REQUIRED carries a resource")
	}
	if _, ok := recvWatchAllowClose(t, stream2, 10*time.Second); ok {
		t.Error("stream stayed open after RESYNC_REQUIRED")
	}
	if err := stream2.Err(); err != nil {
		t.Errorf("stream error after RESYNC_REQUIRED: %v, want a clean close", err)
	}
}

// A token older than the bounded change log ends the stream with
// RESYNC_REQUIRED/TOKEN_TOO_OLD, and the close is clean.
func TestWatchEvictedTokenNeedsResync(t *testing.T) {
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
	ctx2, cancel2 := context.WithCancel(t.Context())
	defer cancel2()
	stream2, err := e.client.Watch(ctx2, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   token,
	}))
	if err != nil {
		t.Fatalf("Watch resume: %v", err)
	}
	resync, ok := recvWatchAllowClose(t, stream2, 10*time.Second)
	if !ok {
		t.Fatalf("stream closed without RESYNC_REQUIRED: %v", stream2.Err())
	}
	if resync.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED ||
		resync.GetResyncReason() != hubv1.ResyncReason_RESYNC_REASON_TOKEN_TOO_OLD {
		t.Errorf("got %v/%v, want RESYNC_REQUIRED/TOKEN_TOO_OLD",
			resync.GetChangeType(), resync.GetResyncReason())
	}
	if _, ok := recvWatchAllowClose(t, stream2, 10*time.Second); ok {
		t.Error("stream stayed open after RESYNC_REQUIRED")
	}
	if err := stream2.Err(); err != nil {
		t.Errorf("stream error after RESYNC_REQUIRED: %v, want a clean close", err)
	}
	if got := sample(e.scrape(t), "fleet_hub_watch_resync_total", `reason="RESYNC_REASON_TOKEN_TOO_OLD"`); got != 1 {
		t.Errorf("fleet_hub_watch_resync_total{TOKEN_TOO_OLD} = %v, want 1", got)
	}
	if opened := sample(e.scrape(t), "fleet_hub_watch_streams_total", `result="opened"`); opened < 1 {
		t.Errorf("fleet_hub_watch_streams_total{opened} = %v, want at least 1", opened)
	}
	if resyncs := e.logs.events(t, "watch.resync"); len(resyncs) != 1 || resyncs[0]["reason"] != "TOKEN_TOO_OLD" {
		t.Errorf("watch.resync events = %v, want one TOKEN_TOO_OLD", resyncs)
	}
	if opened := e.logs.events(t, "watch.opened"); len(opened) < 1 {
		t.Errorf("watch.opened events = %v, want at least 1", len(opened))
	}
}

// A token that does not parse is INVALID_ARGUMENT/RESUME_TOKEN_INVALID.
func TestWatchMalformedTokenIsInvalidArgument(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	stream, serr := e.client.Watch(t.Context(), connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_REPOS, "", false)},
		ResumeToken:   "not-a-resume-token",
	}))
	if code, reason, _ := detail(t, openWatchErr(stream, serr)); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_RESUME_TOKEN_INVALID {
		t.Errorf("got %v %v, want invalid_argument RESUME_TOKEN_INVALID", code, reason)
	}
}

func TestWatchValidation(t *testing.T) {
	t.Parallel()
	repos := hubv1.WatchTopic_WATCH_TOPIC_REPOS
	cases := []struct {
		name   string
		subs   []*hubv1.TopicSubscription
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{
			"duplicate topic",
			[]*hubv1.TopicSubscription{watchSub(repos, "", true), watchSub(repos, "", true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"unknown filter field",
			[]*hubv1.TopicSubscription{watchSub(repos, `bogus = 1`, true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FILTER_INVALID,
		},
		{
			"summary filter refused",
			[]*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, `id = "x"`, true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FILTER_INVALID,
		},
		{
			"routines not served yet",
			[]*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_ROUTINES, "", true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"devices are v3",
			[]*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_DEVICES, "", true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"unspecified topic",
			[]*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_UNSPECIFIED, "", true)},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
	}
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream, serr := e.client.Watch(t.Context(), connect.NewRequest(&hubv1.WatchRequest{
				Subscriptions: tc.subs,
			}))
			if code, reason, _ := detail(t, openWatchErr(stream, serr)); code != tc.code || reason != tc.reason {
				t.Errorf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

func TestWatchDeniedPaths(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", user, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			stream, serr := e.client.Watch(t.Context(), connect.NewRequest(&hubv1.WatchRequest{}))
			if code, reason, _ := detail(t, openWatchErr(stream, serr)); code != tc.code || reason != tc.reason {
				t.Errorf("got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

// Empty subscriptions mean every served topic except pane summaries,
// unfiltered, with send_initial.
func TestWatchEmptySubscriptionsServeEveryTopicButPanes(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "r-1", "github.com", "acme/api")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// Every served topic but pane summaries completes its initial state.
	wantDone := map[hubv1.WatchTopic]bool{
		hubv1.WatchTopic_WATCH_TOPIC_MACHINES:        true,
		hubv1.WatchTopic_WATCH_TOPIC_SESSIONS:        true,
		hubv1.WatchTopic_WATCH_TOPIC_GOALS:           true,
		hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS:      true,
		hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS: true,
		hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS:        true,
		hubv1.WatchTopic_WATCH_TOPIC_TRIAGE:          true,
		hubv1.WatchTopic_WATCH_TOPIC_APPROVALS:       true,
		hubv1.WatchTopic_WATCH_TOPIC_SETTINGS:        true,
		hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS:      true,
		hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS:    true,
		hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY:   true,
		hubv1.WatchTopic_WATCH_TOPIC_AUDIT:           true,
		hubv1.WatchTopic_WATCH_TOPIC_REPOS:           true,
	}
	doneTopics := map[hubv1.WatchTopic]bool{}
	sawRepoInitial := false
	deadline := time.Now().Add(10 * time.Second)
	for len(doneTopics) < len(wantDone) && time.Now().Before(deadline) {
		m, ok := recvWatchAllowClose(t, stream, 10*time.Second)
		if !ok {
			t.Fatalf("stream closed: %v", stream.Err())
		}
		switch m.GetChangeType() {
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL:
			if m.GetTopic() == hubv1.WatchTopic_WATCH_TOPIC_REPOS {
				sawRepoInitial = true
			}
			if m.GetResumeToken() == "" {
				t.Error("INITIAL carries no resume_token")
			}
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL_DONE:
			if doneTopics[m.GetTopic()] {
				t.Fatalf("second INITIAL_DONE for %v", m.GetTopic())
			}
			doneTopics[m.GetTopic()] = true
			if m.GetTopic() == hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES {
				t.Error("pane summaries arrive without a subscription")
			}
			if !wantDone[m.GetTopic()] {
				t.Errorf("INITIAL_DONE for unexpected topic %v", m.GetTopic())
			}
		default:
			t.Fatalf("got %v, want INITIAL or INITIAL_DONE", m.GetChangeType())
		}
	}
	for topic := range wantDone {
		if !doneTopics[topic] {
			t.Errorf("no INITIAL_DONE for %v", topic)
		}
	}
	if !sawRepoInitial {
		t.Error("no INITIAL for the watched repo")
	}
}
