package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// testCommitFleet builds a Fleet with a real store for writer-ordering
// tests: hook publishes need the single writer, so a zero Fleet no longer
// suffices.
func testCommitFleet(t *testing.T) *Fleet {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet", CACert: []byte("c"), CAKey: []byte("k"), ServerCert: []byte("c"), ServerKey: []byte("k"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "e", CreatedTime: time.Now(),
	}
	ctx := context.Background()
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	st, err := statedb.Open(ctx, dir, tel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f, err := NewFleet(FleetOptions{Store: st, CACert: []byte("c"), Telemetry: tel, HubInstanceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// testLogger is a Watch session logger that drops its lines: the tests
// assert messages, and the FELL_BEHIND log test captures its own.
func testLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// busEntry builds a never-dropped log entry at one fake-clock reading.
func busEntry(at time.Time, seq uint64, topic hubv1.WatchTopic) watchEntry {
	return watchEntry{at: at, seq: seq, change: watchChange{
		topic: topic, change: hubv1.ChangeType_CHANGE_TYPE_CREATED,
		msg:   &hubv1.WatchResponse{Topic: topic, ChangeType: hubv1.ChangeType_CHANGE_TYPE_CREATED},
		match: func(*filterNode) bool { return true },
	}}
}

// The change log keeps the larger of the time and count guarantees: fresh
// entries survive past the count cap (P2-9), and only entries older than
// the floor leave once the log overflows (then the floor tracks them).
func TestWatchBusRetainsTimeAndCount(t *testing.T) {
	t.Parallel()
	base := time.Now()
	nowNanos := base.UnixNano()
	clock := func() time.Time { return time.Unix(0, nowNanos) }
	bus := newWatchBus(clock, 2)
	at := func() time.Time { return clock() }
	bus.publish(busEntry(at(), 1, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	bus.publish(busEntry(at(), 2, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	bus.publish(busEntry(at(), 3, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	if got := len(bus.log); got != 3 {
		t.Fatalf("fresh log = %d entries, want 3 (no eviction while young)", got)
	}
	if got := bus.evictedSeq(); got != 0 {
		t.Fatalf("evicted floor = %d, want 0 (nothing evicted)", got)
	}
	nowNanos = base.Add(11 * time.Minute).UnixNano()
	bus.publish(busEntry(at(), 4, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	entries, _, _ := bus.snapshot(0, 4)
	if len(entries) != 2 || entries[0].seq != 3 || entries[1].seq != 4 {
		t.Fatalf("log after aging = %v, want seqs [3 4]", seqsOf(entries))
	}
	if got := bus.evictedSeq(); got != 2 {
		t.Errorf("evicted floor = %d, want 2", got)
	}
}

func seqsOf(entries []watchEntry) []uint64 {
	out := make([]uint64, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.seq)
	}
	return out
}

// A token below the oldest retained entry still replays when nothing
// evicted: the floor, not the oldest entry, decides (P2-8). Gaps from
// unpublished stamps are normal, not evidence of loss.
func TestWatchBusEvictionFloor(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	bus.publish(busEntry(now, 100, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	bus.publish(busEntry(now, 101, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	if got := bus.evictedSeq(); got != 0 {
		t.Fatalf("evicted floor = %d, want 0 (nothing evicted)", got)
	}
	// Token 50 sits below the oldest retained entry (100) with no
	// eviction: the log still covers everything after it.
	entries, _, _ := bus.snapshot(50, 101)
	if len(entries) != 2 {
		t.Fatalf("replay from 50 = %d entries, want 2", len(entries))
	}
	// Old entries past the cap do evict, and the floor tracks them.
	old := newWatchBus(func() time.Time { return now }, 1)
	old.publish(busEntry(now.Add(-time.Hour), 1, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	old.publish(busEntry(now.Add(-time.Hour), 2, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	old.publish(busEntry(now, 3, hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	if got := old.evictedSeq(); got != 2 {
		t.Errorf("evicted floor = %d, want 2", got)
	}
	entries, _, _ = old.snapshot(2, 3)
	if len(entries) != 1 || entries[0].seq != 3 {
		t.Errorf("replay from the floor = %v, want [3]", seqsOf(entries))
	}
}

// Entries on unsubscribed topics never queue and never flag the stream
// behind; only a subscribed topic's overflow ends it (P2-10).
func TestWatchBusFiltersUnsubscribedTopics(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	sub, _ := bus.subscribe([]hubv1.WatchTopic{hubv1.WatchTopic_WATCH_TOPIC_REPOS})
	for i := range 300 {
		bus.publish(busEntry(now, uint64(i+1), hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS))
	}
	if sub.isBehind() {
		t.Fatal("300 unsubscribed changes flagged the stream behind")
	}
	select {
	case e := <-sub.ch:
		t.Fatalf("unsubscribed change %v queued on the stream", e.change.topic)
	default:
	}
	// The subscribed topic still ends a full stream behind.
	for i := range 300 {
		bus.publish(busEntry(now, uint64(i+1001), hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	}
	if !sub.isBehind() {
		t.Fatal("300 subscribed changes past the buffer did not flag the stream behind")
	}
}

// Shed values on unsubscribed topics never reach the stash either: a full
// REPOS-only stream keeps no pane or summary it did not ask for (P2-10).
func TestWatchBusDropsUnsubscribedShed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	sub, _ := bus.subscribe([]hubv1.WatchTopic{hubv1.WatchTopic_WATCH_TOPIC_REPOS})
	// Fill the channel so the shed path runs.
	for len(sub.ch) < watchSubBuf {
		bus.publish(busEntry(now, uint64(len(sub.ch)+1), hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	}
	bus.publishPane("default/s1/%1", busEntry(now, 1001, hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES))
	bus.publishSummary(busEntry(now, 1002, hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY))
	panes, summary := sub.drainStash()
	if len(panes) != 0 || summary != nil {
		t.Fatalf("stash holds unsubscribed shed values: %d panes, summary %v", len(panes), summary != nil)
	}
	// The pane map itself still collapses the latest per pane, and a
	// stale summary never overwrites a newer one.
	bus.publishPane("k", busEntry(now, 5, hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES))
	bus.publishPane("k", busEntry(now, 4, hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES))
	if got := bus.panes["k"].seq; got != 5 {
		t.Errorf("pane latest = %d, want 5", got)
	}
	bus.publishSummary(busEntry(now, 1005, hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY))
	bus.publishSummary(busEntry(now, 1004, hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY))
	if got := bus.summary.seq; got != 1005 {
		t.Errorf("summary latest = %d, want 1005", got)
	}
}

// WriteHook runs one commit with its publish as an ordered step on the
// single writer: a second job queues behind the first instead of
// interleaving, so the bus appends in commit order (#393).
func TestWriteHookSerializesPublish(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	doneA := make(chan struct{})
	doneB := make(chan struct{})
	go func() {
		defer close(doneA)
		_ = f.store.WriteHook(context.Background(),
			func(tx *sql.Tx) error {
				record("commit-A")
				close(entered)
				<-release
				return nil
			},
			func() { record("publish-A") },
		)
	}()
	<-entered
	go func() {
		defer close(doneB)
		_ = f.store.WriteHook(context.Background(),
			func(tx *sql.Tx) error { record("commit-B"); return nil },
			func() { record("publish-B") },
		)
	}()
	// Let B reach the mutex while A holds it, then release A. Without
	// the serialization B would commit and publish between A's commit
	// and A's publish. Either scheduling with it gives the same order:
	// B runs wholly after A.
	time.Sleep(100 * time.Millisecond)
	close(release)
	<-doneA
	<-doneB
	want := []string{"commit-A", "publish-A", "commit-B", "publish-B"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// A slow consumer reaches FELL_BEHIND straight from the session loop,
// and the shared resync path logs it with the actor and the reason
// (P1-5), like the TOKEN_TOO_OLD and HUB_RESTARTED paths do.
func TestWatchFellBehindLogsResync(t *testing.T) {
	t.Parallel()
	now := time.Now()
	bus := newWatchBus(func() time.Time { return now }, 0)
	sub, _ := bus.subscribe([]hubv1.WatchTopic{hubv1.WatchTopic_WATCH_TOPIC_REPOS})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	stub := newStubSend()
	sess := &watchSession{
		bus: bus, instanceID: "test-hub", subs: []*watchSubscription{
			{def: watchTopics[hubv1.WatchTopic_WATCH_TOPIC_REPOS]},
		},
		sub: sub, send: stub.send, now: func() time.Time { return now },
		checkpoint: time.Hour, resyncs: testResyncs(t), logger: logger,
	}
	// Overflow the buffer with never-dropped changes, so the stream is
	// behind before the session reads its first entry: the live loop
	// resyncs without sending anything first.
	for i := range watchSubBuf + 10 {
		bus.publish(busEntry(now, uint64(i+1), hubv1.WatchTopic_WATCH_TOPIC_REPOS))
	}
	if !sub.isBehind() {
		t.Fatal("full buffer did not flag the stream behind")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sess.run(ctx) }()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	var logged bool
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var m map[string]string
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if m["msg"] == "watch.resync" && m["reason"] == "FELL_BEHIND" {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no watch.resync/FELL_BEHIND log line, got %q", buf.String())
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
}

// Thirty-two concurrent writer jobs each publish in their commit hook:
// no hook ever runs before its own commit, and hooks run in the same
// order as their commits, so the bus order matches the commit order on
// every interleaving (#393).
func TestWriteHookOrdersManyWriters(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	const writers = 32
	var mu sync.Mutex
	var order []string
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = f.store.WriteHook(context.Background(),
				func(tx *sql.Tx) error {
					time.Sleep(time.Millisecond)
					record(fmt.Sprintf("commit-%d", id))
					return nil
				},
				func() { record(fmt.Sprintf("publish-%d", id)) },
			)
		}(i)
	}
	wg.Wait()
	if len(order) != 2*writers {
		t.Fatalf("order has %d steps, want %d", len(order), 2*writers)
	}
	commitPos := map[string]int{}
	publishOrder := []string{}
	for pos, step := range order {
		if len(step) > 7 && step[:7] == "commit-" {
			commitPos[step[7:]] = pos
		} else if len(step) > 8 && step[:8] == "publish-" {
			publishOrder = append(publishOrder, step[8:])
		} else {
			t.Fatalf("step %d = %q, want commit-N or publish-N", pos, step)
		}
	}
	if len(commitPos) != writers || len(publishOrder) != writers {
		t.Fatalf("order = %v, want %d commits and %d publishes", order, writers, writers)
	}
	var commitOrder []string
	for _, step := range order {
		if len(step) > 7 && step[:7] == "commit-" {
			commitOrder = append(commitOrder, step[7:])
		}
	}
	for i, id := range publishOrder {
		if commitOrder[i] != id {
			t.Fatalf("publish order %v does not match commit order %v", publishOrder, commitOrder)
		}
		if commitPos[id] > indexOf(order, "publish-"+id) {
			t.Fatalf("publish-%s runs before its commit", id)
		}
	}
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
