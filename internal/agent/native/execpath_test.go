package native_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/native"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// execRig runs the real executor with the host's handlers: the production
// caller (envelope checks, command log, handler dispatch) with fakes only
// at the outer seams (stream, clock, policy, audit).
type execRig struct {
	ex   *exec.Executor
	send *rigSender
	tel  *telemetry.Telemetry
}

type rigSender struct {
	mu   sync.Mutex
	msgs []*agentv1.ConnectRequest
	more chan struct{}
}

func (s *rigSender) Send(_ context.Context, m *agentv1.ConnectRequest) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, proto.Clone(m).(*agentv1.ConnectRequest)) //nolint:forcetypeassert // Clone keeps the type.
	s.mu.Unlock()
	select {
	case s.more <- struct{}{}:
	default:
	}
	return nil
}

func (s *rigSender) all() []*agentv1.ConnectRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentv1.ConnectRequest(nil), s.msgs...)
}

type rigClock struct{ now time.Time }

func (c *rigClock) Now() (time.Time, bool) { return c.now, true }

type rigPolicy struct{}

func (rigPolicy) SafeMode() bool               { return false }
func (rigPolicy) RateLimit(string) (int, bool) { return 0, false }
func (rigPolicy) ApprovalRequired(string) bool { return false }

type rigAudit struct{}

func (rigAudit) Append(audit.Entry) {}

type rigApprover struct{ t *testing.T }

func (a rigApprover) Await(_ context.Context, commandID, kind string) (bool, string, error) {
	a.t.Helper()
	a.t.Fatalf("approval asked for %s %s: native commands need none", commandID, kind)
	return false, "", nil
}

var _ conn.HubClock = (*rigClock)(nil)

func newExecRig(t *testing.T, f *fixture) *execRig {
	t.Helper()
	if err := f.db.StartEpoch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "execpath", LogOutput: testLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	send := &rigSender{more: make(chan struct{}, 1)}
	ex, err := exec.New(exec.Options{
		DB: f.db, Sender: send, Clock: &rigClock{now: time.Now()}, Policy: rigPolicy{},
		Audit: rigAudit{}, Handlers: f.h.Handlers(), Approver: rigApprover{t},
		Home: "/home/op", Telemetry: tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ex.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return &execRig{ex: ex, send: send, tel: tel}
}

// result waits for the CommandResult of id through the sender.
func (r *execRig) result(t *testing.T, id string) *agentv1.CommandResult {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		for _, m := range r.send.all() {
			if res := m.GetCommandResult(); res.GetCommandId().GetValue() == id {
				return res
			}
		}
		select {
		case <-r.send.more:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			t.Fatalf("no result for %s", id)
		}
	}
}

// With no qualified adapter the registered handlers stay unadvertised: the
// executor answers the typed command UNSUPPORTED naming the per-backend
// capability, and Hello carries no native capability.
func TestExecutorRefusesUnqualifiedNativeWithNamedCapability(t *testing.T) {
	t.Parallel()
	f := newBareFixture(t)
	r := newExecRig(t, f)
	if caps := r.ex.Capabilities(); len(caps) != 0 {
		t.Fatalf("executor advertises %+v, want nothing", caps)
	}

	cmd := startCmd("command-start")
	if !r.ex.Command(context.Background(), 1, cmd) {
		t.Fatal("the intake refused a command")
	}
	res := r.result(t, "command-start")
	if res.GetStatus() != typev1.Status_STATUS_UNSUPPORTED {
		t.Fatalf("status %v, want UNSUPPORTED", res.GetStatus())
	}
	if res.GetMissingCapability().GetName() != "native.claude_code" {
		t.Fatalf("missing capability %+v, want native.claude_code", res.GetMissingCapability())
	}
}

// With a qualified adapter the production path dispatches the typed command
// from intake to the registered handler to the adapter: one external call,
// one confirmed result.
func TestExecutorDispatchesNativeToAdapter(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := newExecRig(t, f)
	if caps := r.ex.Capabilities(); len(caps) != 1 || caps[0].GetName() != "native.claude_code" {
		t.Fatalf("executor advertises %+v, want native.claude_code", caps)
	}

	cmd := startCmd("command-start")
	if !r.ex.Command(context.Background(), 1, cmd) {
		t.Fatal("the intake refused a command")
	}
	res := r.result(t, "command-start")
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("status=%v reason=%v message=%q", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if got := res.GetStartNativeSession().GetProviderSessionId(); got != "provider-session-1" {
		t.Fatalf("provider id %q", got)
	}
	if calls := f.fa.started(); len(calls) != 1 || calls[0] != "start:session-1" {
		t.Fatalf("adapter calls %v, want one start", calls)
	}
}

// Every native handler fails closed: the executor never resumes one, and
// the defensive Resume answers UNKNOWN_OUTCOME without forging a result.
func TestNativeHandlersNeverResume(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	for _, h := range f.h.Handlers() {
		if h.Kind() == "" || h.Kind() == "unknown" {
			t.Fatalf("handler with no kind: %+v", h)
		}
		if got := h.Class(nil); got != exec.NotRepeatSafe {
			t.Fatalf("%s class %v, want not_repeat_safe", h.Kind(), got)
		}
		res := h.Resume(context.Background(), exec.Request{})
		if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
			t.Fatalf("%s resume status %v, want UNKNOWN_OUTCOME", h.Kind(), res.GetStatus())
		}
	}
	if len(f.h.Handlers()) != 5 {
		t.Fatalf("handlers %d, want the five native kinds", len(f.h.Handlers()))
	}
	if kinds := map[string]bool{}; true {
		for _, h := range f.h.Handlers() {
			kinds[h.Kind()] = true
		}
		for _, want := range []string{"StartNativeSession", "SendNativeTurn", "ResumeNativeSession", "InterruptNativeSession", "StopNativeSession"} {
			if !kinds[want] {
				t.Fatalf("no handler for %s", want)
			}
		}
	}
}

// A handler with no host fails closed instead of panicking.
func TestNativeHandlerWithoutHostFails(t *testing.T) {
	t.Parallel()
	h := native.Handler{}
	res := h.Run(context.Background(), exec.Request{})
	if res.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Fatalf("status %v, want FAILED", res.GetStatus())
	}
}
