package inventory

import (
	"context"
	"strconv"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// A burst of notices is one read a second at most (readGap), and the last
// change still ends up read.
func TestNoticesAreReadAtMostOnceASecond(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachNoticesAreReadAtMostOnceASecond(t, b) })
	}
}

func eachNoticesAreReadAtMostOnceASecond(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
	dev.Run(t, "set-option", "-g", "automatic-rename", "off")
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make(chan string, 64)
	o, err := New(Options{Program: b.Path, Runner: runner, Folder: folder.Dir, Servers: []string{"default"}, Emitter: emitFunc(func(ev *agentv1.Event) {
		if w := ev.GetWindow(); w != nil {
			names <- w.GetWindow().GetName()
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	s := o.servers["default"]
	wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
	defer wcancel()
	if err := s.c.WaitState(wctx, tmux.StateUp); err != nil {
		t.Fatal(err)
	}
	if err := o.Snapshot(wctx, newSnap()); err != nil { // the cold read is done
		t.Fatal(err)
	}
	before, start := s.reads.Load(), time.Now()
	for i := range 20 {
		dev.Run(t, "rename-window", "-t", "work:0", "w"+strconv.Itoa(i))
	}
	time.Sleep(2500 * time.Millisecond) // let the last paced read run
	elapsed := time.Since(start)
	reads := s.reads.Load() - before
	if max := int64(elapsed/readGap) + 2; reads > max {
		t.Fatalf("%d reads in %s for 20 renames, want at most %d (one a second)", reads, elapsed, max)
	}
	// The paced read after the burst saw the last rename, with no Snapshot.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case n := <-names:
			if n == "w19" {
				return
			}
		case <-deadline:
			t.Fatal("no RENAMED event with the last name w19")
		}
	}
}

func newSnap() *agentv1.Snapshot { return &agentv1.Snapshot{} }

type emitFunc func(*agentv1.Event)

func (f emitFunc) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) { f(ev); return 1, nil }
