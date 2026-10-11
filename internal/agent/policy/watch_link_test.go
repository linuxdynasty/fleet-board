package policy_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// linkedRig is a rig whose policy.yaml is a symlink to a file in another
// folder, the way a dotfile manager lays it out.
func linkedRig(t *testing.T) (r *rig, target string) {
	t.Helper()
	r = newRig(t, "")
	target = filepath.Join(t.TempDir(), "policy-real.yaml")
	if err := os.WriteFile(target, []byte(goodFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, r.path); err != nil {
		t.Fatal(err)
	}
	return r, target
}

// inotify reports a write to a link's target only to a watch on the target's
// folder; the config folder's watch never sees it.
func TestAWriteToTheTargetOfALinkedPolicyFileInAnotherFolderIsSeen(t *testing.T) {
	t.Parallel()
	r, target := linkedRig(t)
	m := r.load(t)
	watch(t, m)
	next := goodFile + "max_total: 6\n"
	if err := os.WriteFile(target, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
	// An editor that replaces the target by rename in its own folder.
	again := next + "max_windows: 9\n"
	tmp := filepath.Join(filepath.Dir(target), ".swp")
	if err := os.WriteFile(tmp, []byte(again), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, target); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != sum(again) {
		t.Errorf("Change after a rename over the target = %+v", c)
	}
}

func TestAWriteToALinkTargetInTheConfigFolderIsSeen(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	target := filepath.Join(r.dir, "policy-real.yaml")
	if err := os.WriteFile(target, []byte(goodFile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("policy-real.yaml", r.path); err != nil {
		t.Fatal(err)
	}
	m := r.load(t)
	watch(t, m)
	next := goodFile + "max_total: 7\n"
	if err := os.WriteFile(target, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
}

// Replacing the link itself by a rename is an event inotify reports and
// kqueue does not (it reports only the temporary name), so the watch follows
// the link on Linux and the Snapshot re-hash finds it on macOS.
func TestAReplacedLinkIsFollowedToItsNewTarget(t *testing.T) {
	t.Parallel()
	r, _ := linkedRig(t)
	m := r.load(t)
	watch(t, m)
	second := filepath.Join(t.TempDir(), "second.yaml")
	moved := goodFile + "max_total: 3\n"
	if err := os.WriteFile(second, []byte(moved), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(r.dir, ".link")
	if err := os.Symlink(second, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "linux" {
		m.Recheck(t.Context()) // what a Snapshot does
	}
	if c := r.nextChange(t); c.Hash != sum(moved) {
		t.Errorf("Change after the link moved = %+v", c)
	}
	if runtime.GOOS != "linux" {
		return // the new target's folder is watched once the watch itself saw the link move
	}
	edited := moved + "max_windows: 8\n"
	if err := os.WriteFile(second, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := r.nextChange(t); c.Hash != sum(edited) {
		t.Errorf("Change after an edit of the new target = %+v", c)
	}
}

// A change in the last settle window is looked at once when the watch ends.
func TestAChangeInTheLastSettleWindowIsSeenWhenTheWatchEnds(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	fake := newFake()
	m := r.withWatcher(t, fake, time.Hour) // the timer would never fire
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { _ = m.Watch(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for !m.Watching() {
		if time.Now().After(deadline) {
			t.Fatal("the watch was not set")
		}
		time.Sleep(5 * time.Millisecond)
	}
	next := goodFile + "max_total: 4\n"
	r.write(t, next)
	fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Write}
	time.Sleep(50 * time.Millisecond) // the loop has taken the event and armed the timer
	cancel()
	<-done
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v, want the edit found at shutdown", c)
	}
}

// samePath compares folders that may differ by a symlinked prefix (/var and
// /private/var on macOS).
func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// The target's folder is watched before the file is read, so a write made
// after the read always raises an event.
func TestTheTargetsFolderIsWatchedBeforeTheFileIsRead(t *testing.T) {
	t.Parallel()
	r, target := linkedRig(t)
	fake := newFake()
	edited := goodFile + "max_total: 5\n"
	fake.onAdd = func(dir string) {
		if samePath(dir, filepath.Dir(target)) { // a write that lands as the watch is set
			_ = os.WriteFile(target, []byte(edited), 0o600)
		}
	}
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	if c := r.nextChange(t); c.Hash != sum(edited) {
		t.Errorf("first Change = %+v, want the content present once the folder was watched", c)
	}
}

func TestTheNewTargetsFolderIsWatchedBeforeTheFileIsReadAfterTheLinkMoves(t *testing.T) {
	t.Parallel()
	r, target := linkedRig(t)
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	second := filepath.Join(t.TempDir(), "second.yaml")
	if err := os.WriteFile(second, []byte(goodFile+"max_total: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := goodFile + "max_total: 4\n"
	fake.mu.Lock()
	fake.onAdd = func(dir string) {
		if samePath(dir, filepath.Dir(second)) {
			_ = os.WriteFile(second, []byte(edited), 0o600)
		}
	}
	fake.mu.Unlock()
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, r.path); err != nil {
		t.Fatal(err)
	}
	fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Create}
	if c := r.nextChange(t); c.Hash != sum(edited) {
		t.Errorf("Change = %+v, want the content present once the new folder was watched", c)
	}
	// The old target's folder is no longer watched.
	removed := fake.removedDirs()
	if len(removed) != 1 || !samePath(removed[0], filepath.Dir(target)) {
		t.Errorf("Remove calls = %v, want the old target's folder", removed)
	}
}

func TestOnlyTheTargetFileOfALinkStartsASettle(t *testing.T) {
	t.Parallel()
	r, target := linkedRig(t)
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	next := goodFile + "max_total: 8\n"
	if err := os.WriteFile(target, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(target) // the name an event from that folder carries
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(resolved)
	// Another file in the target's folder, and one named policy.yaml there
	// (the link's name in the config folder, not the target): neither counts.
	for _, name := range []string{"other.yaml", policy.File} {
		fake.events <- fsnotify.Event{Name: filepath.Join(dir, name), Op: fsnotify.Write}
	}
	r.noChange(t, 400*time.Millisecond)
	fake.events <- fsnotify.Event{Name: resolved, Op: fsnotify.Write}
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v", c)
	}
}

// While an editor has deleted the target and not yet written it again, the
// link cannot be resolved: the watch on its folder stays.
func TestAMissingTargetKeepsTheWatchOnItsFolder(t *testing.T) {
	t.Parallel()
	r, target := linkedRig(t)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFake()
	m := r.withWatcher(t, fake, 20*time.Millisecond)
	watch(t, m)
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	fake.events <- fsnotify.Event{Name: r.path, Op: fsnotify.Write}
	time.Sleep(300 * time.Millisecond) // the settle passes and the link is looked at
	if got := fake.removedDirs(); len(got) != 0 {
		t.Errorf("Remove calls = %v, want none while the target is missing", got)
	}
	next := goodFile + "max_total: 2\n"
	if err := os.WriteFile(target, []byte(next), 0o600); err != nil {
		t.Fatal(err)
	}
	fake.events <- fsnotify.Event{Name: resolved, Op: fsnotify.Write} // still named: the name is kept too
	if c := r.nextChange(t); c.Hash != sum(next) {
		t.Errorf("Change = %+v, want the recreated target's content", c)
	}
}
