package exec_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// wait bounds every wait for something the executor does on its own
// goroutines; generous, so a loaded machine does not flake.
const wait = 20 * time.Second

var hub0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// hubClock is a strict hub clock: hub0 plus the real time since the rig
// started plus what the test added (a sleep the kernel recorded). ok false
// is a hub time the agent cannot read.
type hubClock struct {
	start time.Time
	added atomic.Int64
	bad   atomic.Bool
}

func (c *hubClock) Now() (time.Time, bool) {
	if c.bad.Load() {
		return time.Time{}, false
	}
	return hub0.Add(time.Since(c.start) + time.Duration(c.added.Load())), true
}

func (c *hubClock) sleep(d time.Duration) { c.added.Add(int64(d)) }

// sender keeps every message the executor queued, in order.
type sender struct {
	mu   sync.Mutex
	msgs []*agentv1.ConnectRequest
	more chan struct{}
}

func (s *sender) Send(_ context.Context, m *agentv1.ConnectRequest) error {
	s.mu.Lock()
	s.msgs = append(s.msgs, proto.Clone(m).(*agentv1.ConnectRequest)) //nolint:forcetypeassert // Clone keeps the type
	s.mu.Unlock()
	select {
	case s.more <- struct{}{}:
	default:
	}
	return nil
}

func (s *sender) all() []*agentv1.ConnectRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentv1.ConnectRequest(nil), s.msgs...)
}

// policy is a fake local policy.
type policy struct {
	safe     atomic.Bool
	limits   map[string]int
	approval map[string]bool
}

func (p *policy) SafeMode() bool { return p.safe.Load() }
func (p *policy) RateLimit(kind string) (int, bool) {
	n, ok := p.limits[kind]
	return n, ok
}
func (p *policy) ApprovalRequired(kind string) bool { return p.approval[kind] }

type auditSink struct {
	mu   sync.Mutex
	rows []audit.Entry
}

func (a *auditSink) Append(e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, e)
}

func (a *auditSink) all() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Entry(nil), a.rows...)
}

type rig struct {
	db    *ring.DB
	dir   string
	clock *hubClock
	send  *sender
	pol   *policy
	audit *auditSink
	logs  *conntest.Buffer
	tel   *telemetry.Telemetry
	ex    *exec.Executor
	stop  func()
}

// newRig opens agent.db at epoch 1 and builds an executor with handlers;
// call start to run it.
func newRig(t *testing.T, hs ...exec.Handler) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := ring.Open(context.Background(), dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.StartEpoch(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	r := &rig{
		db: db, dir: dir, clock: &hubClock{start: time.Now()}, send: &sender{more: make(chan struct{}, 1)},
		pol: &policy{limits: map[string]int{}, approval: map[string]bool{}}, audit: &auditSink{}, logs: &conntest.Buffer{},
	}
	r.build(t, nil, hs...)
	return r
}

func (r *rig) buildWith(t *testing.T, a exec.Approver, hs []*handler) {
	t.Helper()
	var list []exec.Handler
	for _, h := range hs {
		list = append(list, h)
	}
	r.build(t, a, list...)
}

func (r *rig) build(t *testing.T, a exec.Approver, hs ...exec.Handler) {
	t.Helper()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: r.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r.tel = tel
	r.ex, err = exec.New(exec.Options{
		DB: r.db, Sender: r.send, Clock: r.clock, Policy: r.pol, Audit: r.audit, Handlers: hs, Approver: a,
		Home: "/Users/al", Telemetry: tel,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.ex.Run(ctx)
	}()
	r.stop = func() { cancel(); <-done }
	t.Cleanup(r.stop)
}

// give hands c to the executor as the connection loop does.
func (r *rig) give(t *testing.T, c *agentv1.Command) {
	t.Helper()
	if !r.ex.Command(context.Background(), c.GetEpoch(), c) {
		t.Fatal("the intake refused a command")
	}
}

// result waits for the CommandResult of id.
func (r *rig) result(t *testing.T, id string) *agentv1.CommandResult {
	t.Helper()
	deadline := time.After(wait)
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
			t.Fatalf("no result for %s; sent %v", id, r.send.all())
		}
	}
}

// count is how many messages of each body went out for id.
func (r *rig) count(id string) (acks, progress, results int) {
	for _, m := range r.send.all() {
		switch {
		case m.GetCommandAck().GetCommandId().GetValue() == id:
			acks++
		case m.GetCommandProgress().GetCommandId().GetValue() == id:
			progress++
		case m.GetCommandResult().GetCommandId().GetValue() == id:
			results++
		}
	}
	return acks, progress, results
}

// metric is the value of the first sample of name whose labels hold label.
func (r *rig) metric(t *testing.T, name, label string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	for line := range strings.Lines(rec.Body.String()) {
		rest, ok := strings.CutPrefix(line, name)
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '{') || !strings.Contains(rest, label) {
			continue
		}
		if i := strings.LastIndexByte(rest, ' '); i >= 0 {
			return strings.TrimSpace(rest[i+1:])
		}
	}
	return ""
}

// cmd is a command of epoch 1 from an OPERATOR, due in a minute of hub
// time, requiring cap.
func cmd(id string, seq uint64, cap string) *agentv1.Command {
	return &agentv1.Command{
		CommandId: &typev1.CommandId{Value: id}, Epoch: 1, Seq: seq,
		IssuedTime: timestamppb.New(hub0), NotAfterTime: timestamppb.New(hub0.Add(time.Minute)),
		RequestedBy: &typev1.Principal{
			Role: typev1.Role_ROLE_OPERATOR, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
			Id: &typev1.PrincipalId{Value: "device/phone-1"},
		},
		Requires: &typev1.Capability{Name: cap, Version: 1},
		Kind:     &agentv1.Command_CapturePane{CapturePane: &agentv1.CapturePane{}},
	}
}

// handler is a fake handler of one kind.
type handler struct {
	kind    string
	caps    []*typev1.Capability
	class   exec.Class
	block   chan struct{} // when set, Run waits for it to close
	runs    atomic.Int32
	resumes atomic.Int32
	step    atomic.Int32
	result  *agentv1.CommandResult
	// nilResult makes Run return no result at all.
	nilResult bool
}

func (h *handler) Kind() string                       { return h.kind }
func (h *handler) Capabilities() []*typev1.Capability { return h.caps }
func (h *handler) Class(*agentv1.Command) exec.Class  { return h.class }
func (h *handler) Run(ctx context.Context, _ exec.Request) *agentv1.CommandResult {
	h.runs.Add(1)
	if h.block != nil {
		select {
		case <-h.block:
		case <-ctx.Done():
			return nil
		}
	}
	return h.res()
}

func (h *handler) Resume(_ context.Context, r exec.Request) *agentv1.CommandResult {
	h.resumes.Add(1)
	h.step.Store(int32(r.Step)) //nolint:gosec // G115: a test step
	return h.res()
}

func (h *handler) res() *agentv1.CommandResult {
	if h.nilResult {
		return nil
	}
	if h.result != nil {
		return proto.Clone(h.result).(*agentv1.CommandResult) //nolint:forcetypeassert // Clone keeps the type
	}
	return &agentv1.CommandResult{Status: typev1.Status_STATUS_OK}
}

func capturePane(cap string) *handler {
	return &handler{kind: "CapturePane", caps: []*typev1.Capability{{Name: cap, Version: 1}}, class: exec.RepeatSafe}
}
