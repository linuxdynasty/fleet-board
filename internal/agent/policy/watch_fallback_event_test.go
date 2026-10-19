package policy_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestDisappearingWatchChildUsesSnapshotFallbackExactlyOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	db, err := ring.Open(t.Context(), t.TempDir(), ring.Options{Telemetry: r.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	emitter, err := events.New(t.Context(), events.Options{DB: db, Telemetry: r.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(emitter.Close)
	fake := newFake()
	// kqueue can inspect a directory entry after an editor renamed it away.
	fake.addErr = fmt.Errorf("watch child: %w", &os.PathError{Op: "lstat", Path: filepath.Join(r.dir, ".policy.tmp"), Err: os.ErrNotExist})
	opts := r.opts()
	opts.NewWatcher = func() (policy.Watcher, error) { return fake, nil }
	send := events.PolicyChanges(emitter)
	opts.OnChange = func(c policy.Change) { send(c); r.changes <- c }
	manager, err := policy.Load(opts)
	if err != nil {
		t.Fatal(err)
	}
	closeManager := sync.OnceFunc(manager.Close)
	t.Cleanup(closeManager)
	if err := manager.Watch(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !fake.closed || manager.Watching() || !manager.WatchOff() {
		t.Fatalf("failed watch: closed=%v watching=%v off=%v", fake.closed, manager.Watching(), manager.WatchOff())
	}
	if info, err := os.Stat(r.dir); err != nil || !info.IsDir() {
		t.Fatalf("config directory disappeared: %v", err)
	}
	logs := r.events(t, "policy.watch_off")
	if len(logs) != 1 || logs[0]["err_kind"] != "io" {
		t.Fatalf("watch-off logs: %v", logs)
	}
	next := goodFile + "max_total: 6\n"
	r.write(t, next)
	size, err := db.EventsSize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if manager.Info().GetChangedSinceLoad() || size.Events != 0 {
		t.Fatal("failed watch detected an unsolicited change")
	}
	snapshot := &agentv1.Snapshot{}
	if err := manager.Snapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	change := r.nextChange(t)
	if change.PreviousHash != sum(goodFile) || change.Hash != sum(next) {
		t.Fatalf("change hashes: %+v", change)
	}
	if err := manager.Snapshot(t.Context(), &agentv1.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	closeManager() // Drain every accepted callback before checking duplicates.
	rows, err := db.EventsBetween(t.Context(), 0, emitter.Seqs().Last()+1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("saved %d events, want one", len(rows))
	}
	event := &agentv1.Event{}
	if err := proto.Unmarshal(rows[0].Body, event); err != nil {
		t.Fatal(err)
	}
	changed := event.GetPolicyChanged()
	if event.GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP || changed == nil || changed.GetPreviousHash() != sum(goodFile) || changed.GetPolicy().GetHash() != sum(next) || !changed.GetPolicy().GetChangedSinceLoad() {
		t.Fatalf("saved policy event: %v", event)
	}
	if snapshot.GetPolicy().GetHash() != sum(goodFile) || !snapshot.GetPolicy().GetChangedSinceLoad() || manager.Policy().MaxTotal != nil || manager.Policy().MaxActive == nil || *manager.Policy().MaxActive != 3 {
		t.Fatal("Snapshot changed the loaded policy ceiling")
	}
	select {
	case duplicate := <-r.changes:
		t.Fatalf("duplicate change: %+v", duplicate)
	default:
	}
	if strings.Contains(r.logs.String(), r.dir) || strings.Contains(r.logs.String(), ".policy.tmp") {
		t.Fatal("watch setup log leaked the raw child path")
	}
	if got := r.metric(t, "fleet_agent_policy_changed_total"); got != "1" {
		t.Fatalf("policy change counter = %s, want 1", got)
	}
	if logs := r.events(t, "policy.changed"); len(logs) != 1 {
		t.Fatalf("change log count: %d", len(logs))
	}
}
