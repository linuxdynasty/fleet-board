package inventory_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/fsnotify/fsnotify"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

func TestSocketFolder(t *testing.T) {
	t.Parallel()
	env := func(v string) func(string) string { return func(string) string { return v } }
	if got := inventory.SocketFolder(env(""), 501); got != "/tmp/tmux-501" {
		t.Fatalf("no TMUX_TMPDIR: %q", got)
	}
	if got := inventory.SocketFolder(env("/run/user/1000"), 1000); got != "/run/user/1000/tmux-1000" {
		t.Fatalf("TMUX_TMPDIR: %q", got)
	}
}

// A server whose socket is created in the watched folder raises a connect
// with no timer: the client waits absent, the socket's Create wakes it
// (row-7 ruling proof for 7d), and the server start is one CREATED event.
func TestSocketCreatedRaisesAConnect(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat") // its socket is "s": not watched
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateAbsent)
			r.snapshot(t) // the sync loop has seen the absence: events start
			folder.Sibling(t, "default", "-s", "work", "exec cat")
			r.waitState(t, "default", tmux.StateUp)
			ev, _ := r.rec.waitFor(t, 0, "TmuxServer CREATED", func(ev *agentv1.Event) bool {
				return serverKind(ev, agentv1.ChangeKind_CHANGE_KIND_CREATED)
			})
			srv := ev.GetTmuxServer().GetServer()
			if srv.GetSocketName() != "default" || !srv.GetRunning() || !srv.GetObserved() || len(srv.GetSessions()) != 1 ||
				srv.GetServerStartId() != r.o.Client("default").ServerStartID() {
				t.Fatalf("CREATED server = %v", srv)
			}
			if got := r.metric(t, "fleet_agent_tmux_servers", `kind="default"`); got != "1" {
				t.Fatalf("fleet_agent_tmux_servers{kind=default} = %q, want 1", got)
			}
			if seen := r.logs.named("tmux.server_seen"); len(seen) != 1 || seen[0]["server_kind"] != "default" {
				t.Fatalf("tmux.server_seen = %v", seen)
			}
		})
	}
}

// fakeWatcher lets a test send watch events by hand.
type fakeWatcher struct {
	events chan fsnotify.Event
	errs   chan error
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{events: make(chan fsnotify.Event), errs: make(chan error)}
}

func (f *fakeWatcher) Add(string) error              { return nil }
func (f *fakeWatcher) Remove(string) error           { return nil }
func (f *fakeWatcher) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeWatcher) Errors() <-chan error          { return f.errs }
func (f *fakeWatcher) Close() error                  { return nil }

// fsnotify can send a Create again for a socket that exists (kqueue
// re-lists a changed folder, row-7 ruling fork 4(c)): while the client is
// attached that is a no-op, with no second attach.
func TestRepeatCreateWhileAttachedIsANoOp(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachRepeatCreateWhileAttachedIsANoOp(t, b) })
	}
}

func eachRepeatCreateWhileAttachedIsANoOp(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	folder.Sibling(t, "default", "-s", "work", "exec cat")
	fw := newFakeWatcher()
	r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
		o.NewWatcher = func() (inventory.Watcher, error) { return fw, nil }
	}, "default")
	r.waitState(t, "default", tmux.StateUp)
	for range 5 {
		fw.events <- fsnotify.Event{Name: filepath.Join(folder.Dir, "default"), Op: fsnotify.Create}
	}
	r.snapshot(t) // the watch goroutine took all five before this rescan token
	if n := len(r.logs.named("tmux.attached")); n != 1 {
		t.Fatalf("tmux.attached lines = %d, want 1 (a repeat Create must not re-attach)", n)
	}
	if n := len(r.logs.named("tmux.exit")); n != 0 {
		t.Fatalf("tmux.exit lines = %d, want 0", n)
	}
}

// With no watch the observer does not poll: it logs tmux.watch_off once,
// and a server that appears is found at the next rescan (a Snapshot).
func TestWatchOffFallsBackToRescansOnly(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachWatchOffFallsBackToRescansOnly(t, b) })
	}
}

func eachWatchOffFallsBackToRescansOnly(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	r := startObserver(t, b, folder.Dir, func(o *inventory.Options) {
		o.NewWatcher = func() (inventory.Watcher, error) { return nil, syscall.EMFILE }
	}, "default")
	r.waitState(t, "default", tmux.StateAbsent)
	if !r.o.WatchOff() {
		t.Fatal("WatchOff() false with no watch")
	}
	off := r.logs.named("tmux.watch_off")
	if len(off) != 1 || off[0]["err_kind"] != "constraint" {
		t.Fatalf("tmux.watch_off = %v", off)
	}
	folder.Sibling(t, "default", "-s", "work", "exec cat")
	r.snapshot(t) // the rescan wakes the client
	r.waitState(t, "default", tmux.StateUp)
}

// A missing socket folder is made the way tmux makes it (0700) and watched.
func TestMissingFolderIsMadeAndWatched(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachMissingFolderIsMadeAndWatched(t, b) })
	}
}

func eachMissingFolderIsMadeAndWatched(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	dir := filepath.Join(folder.Dir, "tmux-test")
	r := startObserver(t, b, dir, nil, "default")
	r.waitState(t, "default", tmux.StateAbsent)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder = %v, %v; want a 0700 folder", fi, err)
	}
	if r.o.WatchOff() || r.o.Folder() != dir {
		t.Fatalf("WatchOff %v, Folder %q", r.o.WatchOff(), r.o.Folder())
	}
}
