package inventory_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func TestMain(m *testing.M) { os.Exit(tmuxtest.Main(m)) }

// wait bounds anything a test waits on: generous, so a loaded machine
// does not flake.
const wait = 30 * time.Second

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// named returns the log lines whose msg is name.
func (l *logBuf) named(name string) []map[string]any {
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(l.String()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

// recorder is an Emitter that keeps every event, stamped as the real one
// stamps it (so an event the delivery table does not know fails here).
type recorder struct {
	t     *testing.T
	mu    sync.Mutex
	evs   []*agentv1.Event
	added chan struct{}
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	return &recorder{t: t, added: make(chan struct{}, 1)}
}

func (r *recorder) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	if err := events.Stamp(ev); err != nil {
		r.t.Errorf("Stamp(%v): %v", ev, err)
	}
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	n := len(r.evs)
	r.mu.Unlock()
	select {
	case r.added <- struct{}{}:
	default:
	}
	return uint64(n), nil
}

func (r *recorder) all() []*agentv1.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*agentv1.Event(nil), r.evs...)
}

// waitFor waits until an event from index from on satisfies ok and returns
// it and the index after it.
func (r *recorder) waitFor(t *testing.T, from int, what string, ok func(*agentv1.Event) bool) (*agentv1.Event, int) {
	t.Helper()
	deadline := time.After(wait)
	for {
		evs := r.all()
		for i := from; i < len(evs); i++ {
			if ok(evs[i]) {
				return evs[i], i + 1
			}
		}
		select {
		case <-r.added:
		case <-deadline:
			t.Fatalf("no %s within %s; events: %v", what, wait, evs)
		}
	}
}

type obsRig struct {
	o    *inventory.Observer
	rec  *recorder
	tel  *telemetry.Telemetry
	logs *logBuf
}

// startObserver runs an observer on folder for servers until the test ends.
func startObserver(t *testing.T, b tmuxtest.Binary, folder string, edit func(*inventory.Options), servers ...string) *obsRig {
	t.Helper()
	logs := &logBuf{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "b", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t)
	opts := inventory.Options{Program: b.Path, Runner: runner, Telemetry: tel, Emitter: rec, Folder: folder, Servers: servers}
	if edit != nil {
		edit(&opts)
	}
	o, err := inventory.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { o.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(wait):
			t.Error("Run did not return after its context ended")
		}
		_ = tel.Shutdown(context.Background())
	})
	return &obsRig{o: o, rec: rec, tel: tel, logs: logs}
}

func (r *obsRig) waitState(t *testing.T, server string, s tmux.State) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := r.o.Client(server).WaitState(ctx, s); err != nil {
		t.Fatalf("%s: waiting for %s (now %s): %v", server, s, r.o.Client(server).State(), err)
	}
}

func (r *obsRig) snapshot(t *testing.T) *agentv1.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	snap := &agentv1.Snapshot{}
	if err := r.o.Snapshot(ctx, snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func (r *obsRig) metric(t *testing.T, name string, labels ...string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		ok := true
		for _, l := range labels {
			ok = ok && strings.Contains(line, l)
		}
		if ok {
			return line[strings.LastIndexByte(line, ' ')+1:]
		}
	}
	return ""
}

func serverKind(ev *agentv1.Event, kind agentv1.ChangeKind) bool {
	return ev.GetTmuxServer() != nil && ev.GetTmuxServer().GetKind() == kind
}

func windowKind(ev *agentv1.Event, kind agentv1.ChangeKind) bool {
	return ev.GetWindow() != nil && ev.GetWindow().GetKind() == kind
}
