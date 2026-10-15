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
	// Setup wakes are not notice reads, so they are settled before the
	// count starts: the input subscription's backup poll reads unpaced
	// until the confirm lands, and last ages past readGap so the burst's
	// first notice reads at once instead of straddling the leading edge.
	cctx, cstop := context.WithTimeout(ctx, 30*time.Second)
	defer cstop()
	for !s.c.SubscriptionConfirmed(inputSub.Name) {
		if cctx.Err() != nil {
			t.Fatal("input subscription never confirmed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(1200 * time.Millisecond)
	before, start := s.reads.Load(), time.Now()
	for i := range 20 {
		dev.Run(t, "rename-window", "-t", "work:0", "w"+strconv.Itoa(i))
	}
	time.Sleep(2500 * time.Millisecond) // let the last paced read run
	elapsed := time.Since(start)
	reads := s.reads.Load() - before
	// Both notice streams share one read clock: server.loop paces dirty
	// (structure changes) and actDirty (window activity) against the same
	// last timestamp, so notice-triggered reads run at most one a second
	// aggregate (trigger.go). A rename can move both streams, but each
	// channel holds one token, so the burst coalesces into an immediate
	// first read and then one a second — at most three paced reads, since
	// the shared clock caps the rate whatever both channels hold. The +3
	// is fixed edge slack, not a doubled rate: up to two setup-edge reads
	// can still land inside the window on a loaded box (a setup notice
	// flushed late, one in-flight backup-poll read from before the
	// confirm). CI run 38676003964 saw 5 reads in 2.546 s with setup
	// wakes counted; a loaded local run with them settled still sees 4.
	// The guard is the collapse — far below one read per rename — and
	// the last rename still read below.
	if max := int64(elapsed/readGap) + 3; reads > max {
		t.Fatalf("%d reads in %s for 20 renames, want at most %d (one a second aggregate)", reads, elapsed, max)
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
