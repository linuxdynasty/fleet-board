package policy_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// watch starts Watch on m and returns once the watch is set.
func watch(t *testing.T, m *policy.Manager) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- m.Watch(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Watch = %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Watch did not return after its context ended")
		}
	})
	deadline := time.Now().Add(10 * time.Second)
	for !m.Watching() {
		if time.Now().After(deadline) {
			t.Fatal("the watch was not set within 10s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTheWatcherSeesAnEditInPlace(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	watch(t, m)
	next := goodFile + "max_total: 6\n"
	r.write(t, next)
	if c := r.nextChange(t); c.PreviousHash != sum(goodFile) || c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
}

func TestTheWatcherSeesAReplaceByRename(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	watch(t, m)
	next := goodFile + "max_total: 7\n"
	tmp := filepath.Join(r.dir, ".policy.yaml.swp")
	if err := os.WriteFile(tmp, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v, want the renamed-in content", c)
	}
	// And the new file is watched too: a second edit in place is seen.
	again := next + "max_windows: 9\n"
	r.write(t, again)
	if c := r.nextChange(t); c.Hash != sum(again) {
		t.Errorf("second Change = %+v", c)
	}
}

func TestTheWatcherSeesADeleteAndARecreate(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	watch(t, m)
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != "" || c.PreviousHash != sum(goodFile) {
		t.Errorf("after delete: Change = %+v", c)
	}
	r.write(t, goodFile)
	if c := r.nextChange(t); c.Hash != sum(goodFile) || c.PreviousHash != "" {
		t.Errorf("after recreate: Change = %+v", c)
	}
	if m.Info().GetChangedSinceLoad() {
		t.Error("the file is back to the loaded content, so changed_since_load is false")
	}
}

func TestTheWatcherSeesAFileCreatedAfterAFirstRun(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	m := r.load(t)
	watch(t, m)
	r.write(t, goodFile)
	if c := r.nextChange(t); c.PreviousHash != "" || c.Hash != sum(goodFile) {
		t.Errorf("Change = %+v", c)
	}
}

func TestAnEditBetweenLoadAndTheWatchIsNotMissed(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	r.write(t, goodFile+"max_total: 5\n") // no watch yet, so no event
	watch(t, m)
	r.nextChange(t)
}

func TestAPolicyFolderRemovedIsSeenAndTheWatchGoesOffWithoutPolling(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	watch(t, m)
	if err := os.RemoveAll(r.dir); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != "" {
		t.Errorf("Change = %+v, want the file gone", c)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !m.WatchOff() {
		if time.Now().After(deadline) {
			t.Fatal("the watch did not go off after its folder was removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ev := r.events(t, "policy.watch_off"); len(ev) != 1 {
		t.Errorf("policy.watch_off = %v, want one", ev)
	}
}

// fakeWatcher is a Watcher the test drives by hand.
type fakeWatcher struct {
	addErr  error
	adds    int
	removes int
	// onAdd, when set, runs inside Add (before it returns) with the folder;
	// removed lists the folders passed to Remove.
	onAdd   func(dir string)
	removed []string
	events  chan fsnotify.Event
	errs    chan error
	mu      sync.Mutex
	closed  bool
}

func newFake() *fakeWatcher {
	return &fakeWatcher{events: make(chan fsnotify.Event, 16), errs: make(chan error, 16)}
}

func (f *fakeWatcher) Add(dir string) error {
	return f.addDir(dir)
}

func (f *fakeWatcher) addDir(dir string) error {
	f.mu.Lock()
	hook := f.onAdd
	f.adds++
	err := f.addErr
	f.mu.Unlock()
	if hook != nil {
		hook(dir)
	}
	return err
}

func (f *fakeWatcher) Remove(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes++
	f.removed = append(f.removed, dir)
	return nil
}

func (f *fakeWatcher) removedDirs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

func (f *fakeWatcher) counts() (adds, removes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adds, f.removes
}

func (f *fakeWatcher) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeWatcher) Errors() <-chan error          { return f.errs }
func (f *fakeWatcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (r *rig) withWatcher(t *testing.T, w policy.Watcher, settle time.Duration) *policy.Manager {
	t.Helper()
	o := r.opts()
	o.Settle = settle
	o.NewWatcher = func() (policy.Watcher, error) { return w, nil }
	m, err := policy.Load(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestAWatchThatCannotBeSetIsLoggedOnceAndNeverBecomesAPoll(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	fake.addErr = syscall.ENOSPC // the inotify watch limit
	m := r.withWatcher(t, fake, 10*time.Millisecond)
	if err := m.Watch(t.Context()); err != nil {
		t.Fatalf("Watch = %v, want nil (the agent runs without a watch)", err)
	}
	if !m.WatchOff() || m.Watching() {
		t.Errorf("WatchOff = %v, Watching = %v", m.WatchOff(), m.Watching())
	}
	if ev := r.events(t, "policy.watch_off"); len(ev) != 1 || ev[0]["err_kind"] != "constraint" {
		t.Errorf("policy.watch_off = %v, want one with err_kind constraint", ev)
	}
	// A change on disk is not noticed on its own: no timer reads the file.
	r.write(t, goodFile+"max_total: 4\n")
	r.noChange(t, 300*time.Millisecond)
	// It is noticed when a Snapshot is built.
	if err := m.Snapshot(t.Context(), &agentv1.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	r.nextChange(t)
	if !fake.closed {
		t.Error("the watcher was not closed")
	}
}

func TestAWatcherThatCannotBeBuiltIsTheSameAsOneThatCannotAdd(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	o := r.opts()
	o.NewWatcher = func() (policy.Watcher, error) { return nil, errors.New("too many open files") }
	m, err := policy.Load(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if err := m.Watch(t.Context()); err != nil || !m.WatchOff() {
		t.Errorf("Watch = %v, WatchOff = %v", err, m.WatchOff())
	}
}

func TestOnlyEventsNamingTheFileStartASettleAndNothingElseReadsIt(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, 50*time.Millisecond)
	watch(t, m)
	next := goodFile + "max_total: 8\n"
	r.write(t, next)
	// No event: the file changed but nothing read it (no polling).
	r.noChange(t, 400*time.Millisecond)
	// An event for another file in the folder: still nothing.
	fake.events <- fsnotify.Event{Name: filepath.Join(r.dir, "notes.txt"), Op: fsnotify.Write}
	r.noChange(t, 400*time.Millisecond)
	// An event for policy.yaml: read once after the settle.
	fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Write}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
	r.noChange(t, 200*time.Millisecond)
}

func TestABurstLongerThanTheSettleIsOneReadAfterTheLastEvent(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	const settle = 600 * time.Millisecond
	m := r.withWatcher(t, fake, settle)
	watch(t, m)
	next := goodFile + "max_total: 8\n"
	r.write(t, next)
	// 12 events 60 ms apart: the burst lasts 1.1 times the settle. A timer
	// that is not restarted by each event (or only by the first) fires 600 ms
	// after the first event, before the last one is sent.
	for range 12 {
		fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Write}
		select {
		case c := <-r.changes:
			t.Fatalf("read before the burst ended: %+v", c)
		case <-time.After(settle / 10):
		}
	}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
	r.noChange(t, settle/2)
}

func TestAFolderPutBackIsWatchedAgainAfterTheOldWatchIsDropped(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	fake.events <- fsnotify.Event{Name: r.dir, Op: fsnotify.Rename}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if adds, removes := fake.counts(); adds >= 2 && removes >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the folder was not dropped and added again")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !m.Watching() || m.WatchOff() {
		t.Errorf("Watching = %v, WatchOff = %v; want the watch still on", m.Watching(), m.WatchOff())
	}
	next := goodFile + "max_total: 9\n"
	// The dropped folder's own re-read may still be pending; an atomic
	// replace means it sees the old file or this one, never a half-written one.
	r.writeAtomic(t, next)
	fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Write}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
}

func TestAFolderThatCannotBeWatchedAgainTurnsTheWatchOff(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	fake.mu.Lock()
	fake.addErr = os.ErrNotExist
	fake.mu.Unlock()
	fake.events <- fsnotify.Event{Name: r.dir, Op: fsnotify.Remove}
	deadline := time.Now().Add(10 * time.Second)
	for !m.WatchOff() {
		if time.Now().After(deadline) {
			t.Fatal("the watch did not go off")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if m.Watching() {
		t.Error("Watching is still true after the watch went off")
	}
}

func TestWatchErrorsAreLoggedOncePerMinute(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, 10*time.Millisecond)
	watch(t, m)
	for range 5 {
		fake.errs <- errors.New("boom")
	}
	time.Sleep(200 * time.Millisecond) // let the loop take them all
	if ev := r.events(t, "policy.watch_error"); len(ev) != 1 {
		t.Errorf("policy.watch_error lines = %d, want 1 (the clock did not move)", len(ev))
	}
	r.advance(2 * time.Minute)
	fake.errs <- errors.New("boom")
	deadline := time.Now().Add(10 * time.Second)
	for len(r.events(t, "policy.watch_error")) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no second line a minute later")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ev := r.events(t, "policy.watch_error"); ev[1]["suppressed"] != float64(4) {
		t.Errorf("second line = %v, want suppressed 4", ev[1])
	}
}

func TestAnEventOverflowIsFollowedByARead(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	next := goodFile + "max_total: 3\n"
	r.write(t, next)
	fake.errs <- fsnotify.ErrEventOverflow
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
	if ev := r.events(t, "policy.watch_error"); len(ev) != 1 || ev[0]["err_kind"] != "constraint" {
		t.Errorf("policy.watch_error = %v", ev)
	}
}

// Watching is true only once the first look at the file is done: a reader
// that sees it can rely on an edit made before the watch having been found.
func TestWatchingIsTrueOnlyAfterTheFirstLookAtTheFile(t *testing.T) {
	t.Parallel()
	for range 20 {
		r := newRig(t, goodFile)
		m := r.load(t)
		r.write(t, goodFile+"max_total: 5\n") // before the watch: no event
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { _ = m.Watch(ctx); close(done) }()
		deadline := time.Now().Add(10 * time.Second)
		for !m.Watching() {
			if time.Now().After(deadline) {
				t.Fatal("the watch was not set within 10s")
			}
			runtime.Gosched()
		}
		if !m.Info().GetChangedSinceLoad() {
			t.Error("Watching was true before the edit made ahead of it was found")
		}
		cancel()
		<-done
	}
}
