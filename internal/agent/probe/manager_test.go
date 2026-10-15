package probe

import (
	"context"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestHandlersDescribe pins the handlers' contract: kinds, the probed
// harnesses' capabilities (OpenCode takes no probe session, so it stays
// unadvertised), and the repeat-safe restart class.
func TestHandlersDescribe(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	m, err := NewManager(testOptions(be, em))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ensure, stop := m.EnsureProbeHandler(), m.StopProbeHandler()
	if ensure.Kind() != "EnsureProbe" || stop.Kind() != "StopProbe" {
		t.Errorf("kinds = %q, %q", ensure.Kind(), stop.Kind())
	}
	caps := map[string]uint32{}
	for _, c := range ensure.Capabilities() {
		caps[c.GetName()] = c.GetVersion()
	}
	for _, name := range []string{"claude", "codex", "muse", "cursor"} {
		if caps["usage.probe."+name] < 1 {
			t.Errorf("capabilities miss usage.probe.%s: %v", name, caps)
		}
	}
	if _, ok := caps["usage.probe.opencode"]; ok {
		t.Errorf("capabilities advertise opencode: %v", caps)
	}
	if len(stop.Capabilities()) != len(ensure.Capabilities()) {
		t.Errorf("stop capabilities differ from ensure capabilities")
	}
	cmd := ensureCommand("p", "a", "k", "codex", true)
	if ensure.Class(cmd) != exec.RepeatSafe || stop.Class(cmd) != exec.RepeatSafe {
		t.Errorf("classes are not repeat-safe")
	}

	// A handler given the other kind fails closed, never dispatching it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mctx, mcancel := context.WithCancel(context.Background())
	defer mcancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(mctx) }()
	res := ensure.Run(ctx, exec.Request{Command: stopCommand("p")})
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("ensure given StopProbe: reason = %v, want INTERNAL", res.GetReason())
	}
	res = stop.Run(ctx, exec.Request{Command: cmd})
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("stop given EnsureProbe: reason = %v, want INTERNAL", res.GetReason())
	}
	if len(be.sessions) != 0 || len(be.sent) != 0 {
		t.Errorf("stop given EnsureProbe touched the backend")
	}
	// Resume continues nothing: it re-runs from the start.
	resumeCmd := ensureCommand("p-resume", "a", "k", "codex", false)
	if res := ensure.Resume(ctx, exec.Request{Command: resumeCmd}); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Errorf("resume status = %v, want OK", res.GetStatus())
	}
	mcancel()
	<-done
}

// TestScheduleRunsDuePasses keeps the declarative promise: a stored
// assignment probes on its interval with no further commands.
func TestScheduleRunsDuePasses(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	ready := loadScreen(t, "../../harness/codex/testdata/0.160.0/ready.txt")
	for range 12 {
		be.captures = append(be.captures, ready)
	}
	opts := testOptions(be, em)
	opts.Now = nil // the schedule runs on the real clock here
	m, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()

	cmd := ensureCommand("probe-sched", "acc-s", "key-s", "codex", false)
	cmd.GetEnsureProbe().Interval = durationpb.New(40 * time.Millisecond)
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reqCancel()
	if res := m.EnsureProbeHandler().Run(reqCtx, exec.Request{Command: cmd}); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("ensure status = %v, want OK", res.GetStatus())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		em.mu.Lock()
		n := len(em.events)
		em.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no scheduled pass ran in 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(be.sent) == 0 {
		t.Errorf("scheduled pass typed nothing")
	}
	// The schedule advances before the pass, so health already names
	// the run after this one.
	health := em.health()
	last := health[len(health)-1]
	if !last.GetNextRunTime().AsTime().After(last.GetLastAttemptTime().AsTime()) {
		t.Errorf("next_run_time %v is not after last_attempt_time %v",
			last.GetNextRunTime().AsTime(), last.GetLastAttemptTime().AsTime())
	}
}

// TestConcurrentEnsuresSerialize runs overlapping commands at one manager:
// the fake backend fails the test if two backend calls ever overlap, and
// -race watches the table. One goroutine owns every probe decision.
func TestConcurrentEnsuresSerialize(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	trust := loadScreen(t, "../../harness/codex/testdata/0.160.0/trust-folder-git.txt")
	for range 16 {
		be.captures = append(be.captures, trust)
	}
	m := runManager(t, be, em)

	var wg sync.WaitGroup
	results := make([]*agentv1.CommandResult, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "probe-a"
			if i%2 == 1 {
				id = "probe-b"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			results[i] = m.EnsureProbeHandler().Run(ctx, exec.Request{Command: ensureCommand(id, "acc", "key", "codex", true)})
		}()
	}
	wg.Wait()
	for i, res := range results {
		if res.GetReason() != typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED {
			t.Errorf("result %d reason = %v, want FOLDER_UNTRUSTED", i, res.GetReason())
		}
	}
}

// TestStopUnknownProbe stays declarative: stopping what was never kept is
// OK and still ends any stray session under its name.
func TestStopUnknownProbe(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	m := runManager(t, be, em)
	res := runStop(t, m, stopCommand("probe-ghost"))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Errorf("stop status = %v, want OK", res.GetStatus())
	}
	if len(be.killed) != 1 {
		t.Errorf("killed sessions = %d, want 1 (the stray session under its name)", len(be.killed))
	}
}

// TestNewManagerRequires fails fast on a half-built manager.
func TestNewManagerRequires(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	full := testOptions(be, em)
	if _, err := NewManager(full); err != nil {
		t.Errorf("full options rejected: %v", err)
	}
	nobe := full
	nobe.Backend = nil
	if _, err := NewManager(nobe); err == nil {
		t.Errorf("nil Backend accepted")
	}
	noem := full
	noem.Events = nil
	if _, err := NewManager(noem); err == nil {
		t.Errorf("nil Events accepted")
	}
	nodir := full
	nodir.ProbeDir = ""
	if _, err := NewManager(nodir); err == nil {
		t.Errorf("empty ProbeDir accepted")
	}
	nolook := full
	nolook.LookPath = nil
	if _, err := NewManager(nolook); err == nil {
		t.Errorf("nil LookPath accepted")
	}
}
