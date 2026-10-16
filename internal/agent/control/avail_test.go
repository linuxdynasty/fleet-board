package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// stubMarks stands in for the observer's availability overlay: it records
// the last mark and answers scripted errors. The overlay's own tests prove
// a recorded mark reaches the Snapshot and emits AvailabilityChanged.
type stubMarks struct {
	pane   *typev1.PaneKey
	avail  *typev1.Availability
	reason typev1.AvailabilityReason
	calls  int
	err    error
}

func (s *stubMarks) ApplyAvailability(pane *typev1.PaneKey, avail *typev1.Availability, reason typev1.AvailabilityReason) error {
	s.calls++
	s.pane, s.avail, s.reason = pane, avail, reason
	return s.err
}

// failRunner refuses every tmux call: the availability mark is pure memory
// (the observer's overlay), so even the OK path never reaches tmux.
type failRunner struct{}

func (failRunner) Run(_ context.Context, _ proc.Spec) (proc.Result, error) {
	return proc.Result{}, errors.New("avail must not call tmux")
}

// availCmd builds a SetPaneAvailability marking pane %1 available with the
// owner's board mark. The shape refusals below fail before the overlay, so
// the dummy socket in testOptions never answers.
func availCmd() *agentv1.SetPaneAvailability {
	return &agentv1.SetPaneAvailability{
		Pane: &typev1.PaneKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			PaneId:        "%1",
		},
		AvailableForWork: true,
		Reason:           typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD,
	}
}

func TestSetAvailabilityRefusesABadShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mute   func(*agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability
		reason typev1.ReasonCode
	}{
		{
			"no command", func(*agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability { return nil },
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"no pane", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability { c.Pane = nil; return c },
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{"another server", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Pane = &typev1.PaneKey{Server: "fleet", ServerStartId: "live-start-id", PaneId: "%1"}
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"bad pane id", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Pane = &typev1.PaneKey{Server: "default", ServerStartId: "live-start-id", PaneId: "1"}
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no server start id", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Pane = &typev1.PaneKey{Server: "default", PaneId: "%1"}
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no reason", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Reason = typev1.AvailabilityReason_AVAILABILITY_REASON_UNSPECIFIED
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"output is reported, never commanded", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Reason = typev1.AvailabilityReason_AVAILABILITY_REASON_OUTPUT
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"idle threshold is reported, never commanded", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Reason = typev1.AvailabilityReason_AVAILABILITY_REASON_IDLE_THRESHOLD
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"goal started is reported, never commanded", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Reason = typev1.AvailabilityReason_AVAILABILITY_REASON_GOAL_STARTED
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"pane changed is reported, never commanded", func(c *agentv1.SetPaneAvailability) *agentv1.SetPaneAvailability {
			c.Reason = typev1.AvailabilityReason_AVAILABILITY_REASON_PANE_CHANGED
			return c
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			marks := &stubMarks{}
			o.Marks = marks
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.SetAvailability(t.Context(), tc.mute(availCmd()))
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("SetAvailability = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
			if marks.calls != 0 {
				t.Fatalf("a refused mark reached the overlay %d times, want none", marks.calls)
			}
		})
	}
}

func TestSetAvailabilityAppliesTheMark(t *testing.T) {
	t.Parallel()
	o := testOptions()
	marks := &stubMarks{}
	o.Marks = marks
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := availCmd()
	cmd.FreshContextOnReuse = true
	res := e.SetAvailability(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("SetAvailability = (%v, %v), want OK: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if marks.calls != 1 {
		t.Fatalf("overlay calls = %d, want 1", marks.calls)
	}
	if marks.pane.GetServer() != "default" || marks.pane.GetPaneId() != "%1" ||
		marks.pane.GetServerStartId() != "live-start-id" {
		t.Fatalf("pane = %v, want default/live-start-id/%%1", marks.pane)
	}
	if !marks.avail.GetAvailableForWork() || !marks.avail.GetFreshContextOnReuse() {
		t.Fatalf("avail = %v, want available with the fresh-context choice kept", marks.avail)
	}
	if marks.reason != typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD {
		t.Fatalf("reason = %v, want MARKED_ON_BOARD", marks.reason)
	}
	if res.GetDetail() != nil {
		t.Fatalf("result carries a detail: %v", res.GetDetail())
	}
}

func TestSetAvailabilityDropsFreshWhenTheMarkIgnoresIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		available bool
		reason    typev1.AvailabilityReason
	}{
		{
			"a clear carries no fresh context", false,
			typev1.AvailabilityReason_AVAILABILITY_REASON_CLEARED_ON_BOARD,
		},
		{
			"a merged goal may always be cleared", true,
			typev1.AvailabilityReason_AVAILABILITY_REASON_GOAL_MERGED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			marks := &stubMarks{}
			o.Marks = marks
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			cmd := availCmd()
			cmd.AvailableForWork = tc.available
			cmd.Reason = tc.reason
			cmd.FreshContextOnReuse = true
			res := e.SetAvailability(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("SetAvailability = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			if marks.avail.GetFreshContextOnReuse() {
				t.Fatalf("avail = %v, want the fresh-context choice dropped", marks.avail)
			}
		})
	}
}

func TestSetAvailabilityMapsAMissingPane(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unknown pane", errors.New(`inventory: pane "%9" is not on server "default"`)},
		{"stale server start", errors.New(`inventory: server "default" is not at start "stale"`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Marks = &stubMarks{err: tc.err}
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.SetAvailability(t.Context(), availCmd())
			if res.GetStatus() != typev1.Status_STATUS_FAILED ||
				res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND {
				t.Fatalf("SetAvailability = (%v, %v), want (FAILED, PANE_NOT_FOUND): %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestSetAvailabilityMakesNoTmuxCall pins the wiring this slice adds: the
// mark is pure memory on the observer's overlay, so even the OK path makes
// no tmux call. A runner that fails every call proves it.
func TestSetAvailabilityMakesNoTmuxCall(t *testing.T) {
	t.Parallel()
	o := testOptions()
	o.Runner = failRunner{}
	marks := &stubMarks{}
	o.Marks = marks
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.SetAvailability(t.Context(), availCmd())
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("SetAvailability = (%v, %v), want OK with no tmux call: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if marks.calls != 1 {
		t.Fatalf("overlay calls = %d, want 1", marks.calls)
	}
}

// recEmitter records every event the observer emits while the test runs.
type recEmitter struct {
	mu  sync.Mutex
	evs []*agentv1.Event
}

func (r *recEmitter) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	return uint64(len(r.evs)), nil
}

func (r *recEmitter) availEdgeTo(paneID string, available bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ev := range r.evs {
		if a := ev.GetAvailability(); a != nil && a.GetPane().GetPaneId() == paneID &&
			a.GetTo().GetAvailableForWork() == available {
			return true
		}
	}
	return false
}

// TestSetAvailabilityReachesTheRealObserver runs the handler against a
// real observer on a throwaway tmux server: the mark the handler records
// emits AvailabilityChanged and shows in the next Snapshot. The stub tests
// above pin the handler's own checks; this one pins the composition the
// suite would otherwise never exercise.
func TestSetAvailabilityReachesTheRealObserver(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			folder.Sibling(t, "default", "-s", "work", "exec cat")
			runner, err := proc.NewRunner(nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := &recEmitter{}
			o, err := inventory.New(inventory.Options{
				Program: b.Path, Runner: runner, Folder: folder.Dir,
				Servers: []string{"default"}, Emitter: rec,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { o.Run(ctx); close(done) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					t.Error("observer Run did not return after its context ended")
				}
			})
			// Wait for the first snapshot carrying the pane.
			var key *typev1.PaneKey
			deadline := time.Now().Add(20 * time.Second)
			for key == nil && time.Now().Before(deadline) {
				snap := &agentv1.Snapshot{}
				sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
				serr := o.Snapshot(sctx, snap)
				scancel()
				if serr == nil {
					for _, srv := range snap.GetTmuxServers() {
						for _, s := range srv.GetSessions() {
							for _, w := range s.GetWindows() {
								if len(w.GetPanes()) > 0 {
									key = w.GetPanes()[0].GetKey()
								}
							}
						}
					}
				}
				if key == nil {
					time.Sleep(50 * time.Millisecond)
				}
			}
			if key == nil {
				t.Fatal("no pane appeared in the observer snapshot")
			}
			// The handler marks through the real overlay while the
			// Ensurer's own tmux socket stays the dummy one, proving
			// again that marking makes no tmux call.
			eo := testOptions()
			eo.Marks = o
			e, err := New(eo)
			if err != nil {
				t.Fatal(err)
			}
			res := e.SetAvailability(ctx, &agentv1.SetPaneAvailability{
				Pane: key, AvailableForWork: true, FreshContextOnReuse: true,
				Reason: typev1.AvailabilityReason_AVAILABILITY_REASON_MARKED_ON_BOARD,
			})
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("SetAvailability = (%v, %v), want OK: %q",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			deadline = time.Now().Add(20 * time.Second)
			for !rec.availEdgeTo(key.GetPaneId(), true) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if !rec.availEdgeTo(key.GetPaneId(), true) {
				t.Fatal("no AvailabilityChanged edge to available after the mark")
			}
			deadline = time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				snap := &agentv1.Snapshot{}
				sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
				serr := o.Snapshot(sctx, snap)
				scancel()
				if serr != nil {
					continue
				}
				if paneAvail(snap, key.GetPaneId()) {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("the snapshot never showed the pane available")
		})
	}
}

// paneAvail reports whether the snapshot marks the pane id available.
func paneAvail(snap *agentv1.Snapshot, paneID string) bool {
	for _, srv := range snap.GetTmuxServers() {
		for _, s := range srv.GetSessions() {
			for _, w := range s.GetWindows() {
				for _, p := range w.GetPanes() {
					if p.GetKey().GetPaneId() == paneID && p.GetAvailability().GetAvailableForWork() {
						return true
					}
				}
			}
		}
	}
	return false
}

// TestAvailabilityHandlerContract pins the executor seam for E11: the kind
// the handler runs, the capability it advertises for Command.requires, and
// the restart class (a mark is declarative and last-write-wins, so a resume
// re-runs it from the start). E11 appears here as a whole word for the
// capability report.
func TestAvailabilityHandlerContract(t *testing.T) {
	t.Parallel()
	o := testOptions()
	o.Marks = &stubMarks{}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	h := e.AvailabilityHandler()
	if h.Kind() != "SetPaneAvailability" {
		t.Fatalf("Kind = %q, want SetPaneAvailability (E11)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. E11 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for E11", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for E11", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_SetPaneAvailability{
		SetPaneAvailability: &agentv1.SetPaneAvailability{},
	}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for E11", got)
	}
	// RepeatSafe resumes by re-running: a shapeless command is refused
	// again, never parked as unknown.
	bad := &agentv1.Command{Kind: &agentv1.Command_SetPaneAvailability{
		SetPaneAvailability: &agentv1.SetPaneAvailability{},
	}}
	res := h.Resume(t.Context(), exec.Request{Command: bad})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("Resume = (%v, %v), want (FAILED, INVALID_ARGUMENT): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	// The wrong kind never reaches the mark: it fails as an internal
	// wiring error, like the F1 to F4 handlers.
	other := &agentv1.Command{Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}}
	res = h.Run(t.Context(), exec.Request{Command: other})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("Run = (%v, %v), want (FAILED, INTERNAL): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if !strings.Contains(res.GetMessage(), "SetPaneAvailability") {
		t.Fatalf("Run message = %q, want it to name SetPaneAvailability", res.GetMessage())
	}
}
