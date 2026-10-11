package settings_test

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

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// wait bounds every wait for the worker goroutine; generous, so a loaded
// machine does not flake.
const wait = 20 * time.Second

// local is a fake policy file in force.
type local struct {
	p       *policy.Policy
	safe    bool
	hash    string
	recheck atomic.Int64
}

func (l *local) Policy() *policy.Policy { return l.p }
func (l *local) SafeMode() bool         { return l.safe }
func (l *local) Info() *agentv1.PolicyInfo {
	return &agentv1.PolicyInfo{Hash: l.hash, SafeMode: l.safe}
}
func (l *local) Recheck(context.Context) { l.recheck.Add(1) }

// sender keeps every SettingsAck queued, in order; fail makes Send refuse.
type sender struct {
	mu   sync.Mutex
	acks []*agentv1.SettingsAck
	fail error
	more chan struct{}
}

func (s *sender) Send(_ context.Context, m *agentv1.ConnectRequest) error {
	s.mu.Lock()
	if s.fail != nil {
		err := s.fail
		s.mu.Unlock()
		return err
	}
	s.acks = append(s.acks, proto.Clone(m.GetSettingsAck()).(*agentv1.SettingsAck)) //nolint:forcetypeassert // Clone keeps the type
	s.mu.Unlock()
	select {
	case s.more <- struct{}{}:
	default:
	}
	return nil
}

func (s *sender) all() []*agentv1.SettingsAck {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentv1.SettingsAck(nil), s.acks...)
}

// rig is a settings Manager on a real agent.db in a temp folder.
type rig struct {
	dir   string
	db    *ring.DB
	local *local
	send  *sender
	logs  *conntest.Buffer
	tel   *telemetry.Telemetry
	m     *settings.Manager
	stop  context.CancelFunc
	done  chan error
}

func newRig(t *testing.T, l *local) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fleet-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: dir}
	r.restart(t, l)
	return r
}

// restart stops the Manager and agent.db if running and starts them again
// over the same file with policy l, as a new agent process would.
func (r *rig) restart(t *testing.T, l *local) {
	t.Helper()
	r.close(t)
	if l.p == nil {
		l.p = &policy.Policy{}
	}
	r.local, r.send, r.logs = l, &sender{more: make(chan struct{}, 1)}, &conntest.Buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: r.logs})
	if err != nil {
		t.Fatal(err)
	}
	r.tel = tel
	ctx := context.Background()
	if r.db, err = ring.Open(ctx, r.dir, ring.Options{}); err != nil {
		t.Fatal(err)
	}
	r.m, err = settings.Open(ctx, settings.Options{DB: r.db, Local: l, Sender: r.send, Telemetry: tel})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var run context.Context
	run, r.stop = context.WithCancel(ctx)
	r.done = make(chan error, 1)
	go func() { r.done <- r.m.Run(run) }()
	t.Cleanup(func() { r.close(t) })
}

func (r *rig) close(t *testing.T) {
	t.Helper()
	if r.stop == nil {
		return
	}
	r.stop()
	select {
	case <-r.done:
	case <-time.After(wait):
		t.Fatal("Run did not return after its context ended")
	}
	r.m.Close()
	_ = r.db.Close()
	_ = r.tel.Shutdown(context.Background())
	r.stop = nil
}

// deliver hands s to the hook and waits for the n-th ack in all.
func (r *rig) deliver(t *testing.T, s *agentv1.Settings, n int) *agentv1.SettingsAck {
	t.Helper()
	if !r.m.Hook(t.Context(), 1, s) {
		t.Fatal("the hook refused a Settings with an empty intake")
	}
	deadline := time.After(wait)
	for {
		if all := r.send.all(); len(all) >= n {
			return all[n-1]
		}
		select {
		case <-r.send.more:
		case <-deadline:
			t.Fatalf("no ack %d within %v", n, wait)
		}
	}
}

func (r *rig) held(t *testing.T) ring.Held {
	t.Helper()
	h, ok, err := r.db.HeldSettings(context.Background())
	if err != nil || !ok {
		t.Fatalf("HeldSettings: %v, %v", ok, err)
	}
	return h
}

func (r *rig) events(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	return conntest.Lines(t, r.logs.String(), prefix)
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
