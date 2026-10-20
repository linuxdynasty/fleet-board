package api

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// This file proves the Watch slow-consumer policy without a network: the
// session runs against a stub send, so a blocked consumer is deterministic.
// Wire coverage of resume and resync lives in watch_test.go.

// stubSend is a watch send that collects, blocks, or releases on demand.
type stubSend struct {
	mu        sync.Mutex
	mode      string // collect or block
	sent      []*hubv1.WatchResponse
	firstCall chan struct{}
	release   chan struct{}
	once      sync.Once
}

func newStubSend() *stubSend {
	return &stubSend{mode: "collect", firstCall: make(chan struct{}), release: make(chan struct{})}
}

func (s *stubSend) send(m *hubv1.WatchResponse) error {
	s.once.Do(func() { close(s.firstCall) })
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	if mode == "block" {
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *stubSend) setMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}

func (s *stubSend) all() []*hubv1.WatchResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*hubv1.WatchResponse{}, s.sent...)
}

func testResyncs(t *testing.T) metric.Int64Counter {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	c, err := tel.Meter("test").Int64Counter("test_resync_total")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testEntry(seq uint64, topic hubv1.WatchTopic, change hubv1.ChangeType, msg *hubv1.WatchResponse) watchEntry {
	return watchEntry{at: time.Now(), seq: seq, change: watchChange{
		topic: topic, change: change, msg: msg,
		match: func(*filterNode) bool { return true },
	}}
}

func paneMsg(server, start, pane, session, machine, lines string) *hubv1.WatchResponse {
	return &hubv1.WatchResponse{
		Topic: hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		Resource: &hubv1.WatchResponse_PaneSummary{PaneSummary: &hubv1.PaneSummary{
			Pane:    &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: pane},
			Session: &typev1.SessionId{Value: session}, Machine: &typev1.MachineId{Value: machine},
			LastLines: lines,
		}},
	}
}

func summaryMsg() *hubv1.WatchResponse {
	return &hubv1.WatchResponse{
		Topic: hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		Resource: &hubv1.WatchResponse_FleetSummary{FleetSummary: &hubv1.FleetSummary{Version: 1}},
	}
}

// A consumer that stops reading keeps only the latest pane and summary
// values; the first never-dropped change past the buffer ends the stream
// with RESYNC_REQUIRED/FELL_BEHIND instead of queueing (02c).
func TestWatchSlowConsumerDropsSummariesOnly(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	sub, _ := bus.subscribe()
	stub := newStubSend()
	stub.setMode("block")
	sess := &watchSession{
		bus: bus, instanceID: "test-hub", subs: []*watchSubscription{
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_REPOS]},
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES]},
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY]},
		},
		sub: sub, send: stub.send, now: func() time.Time { return now },
		checkpoint: time.Hour, resyncs: testResyncs(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.run(ctx) }()
	for i := range 10 {
		seq := uint64(i + 1)
		bus.publish(testEntry(seq, hubv1.WatchTopic_WATCH_TOPIC_REPOS,
			hubv1.ChangeType_CHANGE_TYPE_CREATED,
			&hubv1.WatchResponse{Topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS}))
	}
	<-stub.firstCall
	const neverDropped = 300
	for i := range neverDropped {
		seq := uint64(i + 11)
		bus.publish(testEntry(seq, hubv1.WatchTopic_WATCH_TOPIC_REPOS,
			hubv1.ChangeType_CHANGE_TYPE_CREATED,
			&hubv1.WatchResponse{Topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS}))
	}
	for i := range 50 {
		bus.publishPane("default/s1/%1", testEntry(bus.latest(),
			hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, hubv1.ChangeType_CHANGE_TYPE_UPDATED,
			paneMsg("default", "s1", "%1", "s-1", "m-1", string(rune('a'+i)))))
	}
	bus.publishPane("default/s1/%2", testEntry(bus.latest(),
		hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, hubv1.ChangeType_CHANGE_TYPE_UPDATED,
		paneMsg("default", "s1", "%2", "s-1", "m-1", "other")))
	bus.publishSummary(testEntry(bus.latest(),
		hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, hubv1.ChangeType_CHANGE_TYPE_UPDATED, summaryMsg()))
	if !sub.isBehind() {
		t.Fatal("300 queued changes did not flag the stream behind")
	}
	close(stub.release)
	stub.setMode("collect")
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	sent := stub.all()
	if len(sent) == 0 {
		t.Fatal("nothing sent")
	}
	last := sent[len(sent)-1]
	if last.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED ||
		last.GetResyncReason() != hubv1.ResyncReason_RESYNC_REASON_FELL_BEHIND {
		t.Fatalf("last = %v/%v, want RESYNC_REQUIRED/FELL_BEHIND",
			last.GetChangeType(), last.GetResyncReason())
	}
	var repos, panes, summaries int
	var paneLines []string
	for _, m := range sent[:len(sent)-1] {
		switch m.GetTopic() {
		case hubv1.WatchTopic_WATCH_TOPIC_REPOS:
			repos++
		case hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES:
			panes++
			paneLines = append(paneLines, m.GetPaneSummary().GetLastLines())
		case hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY:
			summaries++
		default:
			t.Errorf("unexpected topic %v on a three-topic stream", m.GetTopic())
		}
	}
	if repos >= neverDropped {
		t.Errorf("repos sent = %d, want fewer than the %d published: nothing shed", repos, neverDropped)
	}
	if panes > 2 {
		t.Errorf("pane summaries sent = %d, want at most 2 (latest per pane)", panes)
	}
	if summaries > 1 {
		t.Errorf("fleet summaries sent = %d, want at most 1", summaries)
	}
	// The surviving pane value is the latest, not the first.
	for _, lines := range paneLines {
		if lines != "" && lines[0] > 'z' {
			t.Errorf("pane lines %q are not from the published set", lines)
		}
	}
}

// FLEET_SUMMARY sends pace to one per second per stream: a burst sends the
// first at once and holds the rest latest-wins (02c).
func TestWatchSummarySendsAtMostOnePerSecond(t *testing.T) {
	t.Parallel()
	now := time.Now()
	var fakeNow atomic.Int64
	fakeNow.Store(now.UnixNano())
	clock := func() time.Time { return time.Unix(0, fakeNow.Load()) }
	bus := newWatchBus(clock, 0)
	sub, _ := bus.subscribe()
	stub := newStubSend()
	sess := &watchSession{
		bus: bus, instanceID: "test-hub", subs: []*watchSubscription{
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY]},
		},
		sub: sub, send: stub.send, now: clock,
		checkpoint: time.Hour, resyncs: testResyncs(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.run(ctx) }()
	for range 5 {
		bus.publishSummary(testEntry(bus.latest(),
			hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, hubv1.ChangeType_CHANGE_TYPE_UPDATED, summaryMsg()))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(stub.all()) < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	// The burst holds: only the first is out within the same second.
	time.Sleep(100 * time.Millisecond)
	if got := len(stub.all()); got != 1 {
		t.Fatalf("summaries sent in one second = %d, want 1", got)
	}
	fakeNow.Add(int64(2 * time.Second))
	bus.publishSummary(testEntry(bus.latest(),
		hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, hubv1.ChangeType_CHANGE_TYPE_UPDATED, summaryMsg()))
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(stub.all()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(stub.all()); got != 2 {
		t.Fatalf("summaries sent = %d, want 2 (first and latest)", got)
	}
}

// An idle stream gets a CHECKPOINT with a fresh token (02c keepalive).
func TestWatchIdleStreamCheckpoints(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	sub, _ := bus.subscribe()
	stub := newStubSend()
	sess := &watchSession{
		bus: bus, instanceID: "test-hub", subs: []*watchSubscription{
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_REPOS]},
		},
		sub: sub, send: stub.send, now: func() time.Time { return now },
		checkpoint: 20 * time.Millisecond, resyncs: testResyncs(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(stub.all()) < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	sent := stub.all()
	if len(sent) == 0 {
		t.Fatal("no CHECKPOINT in 5 s")
	}
	if sent[0].GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CHECKPOINT {
		t.Fatalf("first idle message = %v, want CHECKPOINT", sent[0].GetChangeType())
	}
	if sent[0].GetResumeToken() == "" {
		t.Fatal("CHECKPOINT carries no resume_token")
	}
	token, err := parseWatchToken(sent[0].GetResumeToken())
	if err != nil {
		t.Fatalf("checkpoint token does not parse: %v", err)
	}
	if token.IID != "test-hub" {
		t.Errorf("checkpoint token instance %q, want test-hub", token.IID)
	}
}

func TestReplayOne(t *testing.T) {
	t.Parallel()
	entry := func(change hubv1.ChangeType, match, was bool) watchEntry {
		return watchEntry{seq: 7, change: watchChange{
			topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, change: change,
			msg: &hubv1.WatchResponse{
				Topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, ChangeType: change,
			},
			match: func(*filterNode) bool { return match },
			was:   func(*filterNode) bool { return was },
		}}
	}
	for _, tc := range []struct {
		name   string
		change hubv1.ChangeType
		match  bool
		was    bool
		want   hubv1.ChangeType
		ok     bool
	}{
		{"created match", hubv1.ChangeType_CHANGE_TYPE_CREATED, true, false, hubv1.ChangeType_CHANGE_TYPE_CREATED, true},
		{"created mismatch", hubv1.ChangeType_CHANGE_TYPE_CREATED, false, true, 0, false},
		{"updated match", hubv1.ChangeType_CHANGE_TYPE_UPDATED, true, false, hubv1.ChangeType_CHANGE_TYPE_UPDATED, true},
		{"updated left filter", hubv1.ChangeType_CHANGE_TYPE_UPDATED, false, true, hubv1.ChangeType_CHANGE_TYPE_UNMATCHED, true},
		{"updated unknown past", hubv1.ChangeType_CHANGE_TYPE_UPDATED, false, false, 0, false},
		{"deleted match", hubv1.ChangeType_CHANGE_TYPE_DELETED, true, false, hubv1.ChangeType_CHANGE_TYPE_DELETED, true},
		{"deleted mismatch", hubv1.ChangeType_CHANGE_TYPE_DELETED, false, true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := entry(tc.change, tc.match, tc.was)
			if tc.change == hubv1.ChangeType_CHANGE_TYPE_UPDATED && !tc.was {
				e.change.was = nil
			}
			got, ok := replayOne(e, nil)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got.GetChangeType() != tc.want {
				t.Errorf("change = %v, want %v", got.GetChangeType(), tc.want)
			}
		})
	}
}

func TestWatchTokenRoundTrip(t *testing.T) {
	t.Parallel()
	subs := []watchTokenSub{
		{Topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, Filter: filterHash("")},
		{Topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, Filter: filterHash(`phase = RUNNING`)},
	}
	token := mintWatchToken("hub-1", 41, subs)
	if token == "" {
		t.Fatal("empty token")
	}
	got, err := parseWatchToken(token)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.IID != "hub-1" || got.Seq != 41 || len(got.Subs) != 2 {
		t.Fatalf("token = %+v, want hub-1/41/2 subs", got)
	}
	if !got.covers(hubv1.WatchTopic_WATCH_TOPIC_REPOS, filterHash("")) {
		t.Error("token does not cover the repos subscription")
	}
	if got.covers(hubv1.WatchTopic_WATCH_TOPIC_REPOS, filterHash(`enabled = true`)) {
		t.Error("token covers a changed filter")
	}
	if got.covers(hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, filterHash("")) {
		t.Error("token covers an unsubscribed topic")
	}
	for _, bad := range []string{"", "!!!", mintWatchToken("", 0, nil)} {
		if _, err := parseWatchToken(bad); err == nil {
			t.Errorf("token %q parses, want an error", bad)
		}
	}
}
